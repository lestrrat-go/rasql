package query

import (
	"fmt"
	"reflect"

	"github.com/lestrrat-go/rasql/schema"
)

// ValidationError identifies an invalid part of a query model.
type ValidationError struct {
	Path    string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Path == "" {
		return e.Message
	}
	return fmt.Sprintf("query: %s: %s", e.Path, e.Message)
}

func validationError(path string, format string, args ...any) error {
	return &ValidationError{Path: path, Message: fmt.Sprintf(format, args...)}
}

func validateAlias(alias string) error {
	if err := schema.ValidateIdentifier(alias); err != nil {
		return fmt.Errorf("invalid alias: %w", err)
	}
	return nil
}

func validateExpression(expression Expression, sources map[string]struct{}, path string) error {
	if expression == nil || (reflect.ValueOf(expression).Kind() == reflect.Ptr && reflect.ValueOf(expression).IsNil()) {
		return validationError(path, "must not be nil")
	}

	switch expression := expression.(type) {
	case Column:
		if err := expression.source.validate(); err != nil {
			return validationError(path, "%s", err)
		}
		if _, exists := sources[expression.source.key()]; !exists {
			return validationError(path, "references table %q outside the statement", expression.source.Qualifier())
		}
		if _, exists := expression.source.definition.Column(expression.name); !exists {
			return validationError(path, "references unknown column %q", expression.name)
		}
		return nil
	case ExcludedColumn:
		if err := expression.column.source.validate(); err != nil {
			return validationError(path, "%s", err)
		}
		if _, exists := sources[expression.column.source.key()]; !exists {
			return validationError(path, "references table %q outside the statement", expression.column.source.Qualifier())
		}
		if _, exists := expression.column.source.definition.Column(expression.column.name); !exists {
			return validationError(path, "references unknown column %q", expression.column.name)
		}
		return nil
	case Value:
		return nil
	case Binary:
		if !validBinaryOperator(expression.operator) {
			return validationError(path+".operator", "unsupported operator %q", expression.operator)
		}
		if err := validateExpression(expression.left, sources, path+".left"); err != nil {
			return err
		}
		return validateExpression(expression.right, sources, path+".right")
	case Logical:
		if expression.operator != LogicalAnd && expression.operator != LogicalOr {
			return validationError(path+".operator", "unsupported operator %q", expression.operator)
		}
		if len(expression.expressions) < 2 {
			return validationError(path, "requires at least two expressions")
		}
		for i, child := range expression.expressions {
			if err := validateExpression(child, sources, fmt.Sprintf("%s.expressions[%d]", path, i)); err != nil {
				return err
			}
		}
		return nil
	case Not:
		return validateExpression(expression.expr, sources, path+".expression")
	case NullTest:
		return validateExpression(expression.expr, sources, path+".expression")
	default:
		return validationError(path, "uses unsupported expression %T", expression)
	}
}

func validBinaryOperator(operator BinaryOperator) bool {
	switch operator {
	case OperatorEqual, OperatorNotEqual, OperatorGreaterThan, OperatorGreaterThanOrEqual, OperatorLessThan, OperatorLessThanOrEqual, OperatorLike:
		return true
	default:
		return false
	}
}
