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

// expressionContext tells the expression walk where in a statement the
// expression sits. An aggregate function is only legal in a SELECT projection
// and in the ORDER BY of a statement whose projections all aggregate, and never
// inside another aggregate, so a walk that carries no clause and no nesting
// state cannot tell a legal call from one every dialect rejects.
type expressionContext struct {
	sources map[string]struct{}
	// clause names the SQL clause the expression belongs to, for error messages.
	clause string
	// allowsAggregate reports whether the clause may call an aggregate at all.
	allowsAggregate bool
	// aggregateDepth counts the aggregate calls the walk is currently inside.
	aggregateDepth int
}

// clauseContext returns a context for a clause that must not call an aggregate.
func clauseContext(sources map[string]struct{}, clause string) expressionContext {
	return expressionContext{sources: sources, clause: clause}
}

// aggregateClauseContext returns a context for a clause that may call an
// aggregate. It permits the call itself; a caller that also has to refuse a
// column read outside every aggregate reads bareColumn from the returned usage.
func aggregateClauseContext(sources map[string]struct{}, clause string) expressionContext {
	return expressionContext{sources: sources, clause: clause, allowsAggregate: true}
}

// projectionContext returns a context for a SELECT projection.
func projectionContext(sources map[string]struct{}) expressionContext {
	return aggregateClauseContext(sources, "a SELECT projection")
}

// inAggregate returns a copy of c that walks the arguments of an aggregate call.
func (c expressionContext) inAggregate() expressionContext {
	c.aggregateDepth++
	return c
}

// expressionUsage reports what a validated expression read, so a caller can
// apply rules that span sibling expressions.
type expressionUsage struct {
	// aggregate reports that the expression calls at least one aggregate.
	aggregate bool
	// bareColumn reports that the expression reads a column outside every
	// aggregate call, which no supported dialect can combine with an aggregate
	// while GROUP BY is unsupported.
	bareColumn bool
}

func (u expressionUsage) merge(other expressionUsage) expressionUsage {
	return expressionUsage{
		aggregate:  u.aggregate || other.aggregate,
		bareColumn: u.bareColumn || other.bareColumn,
	}
}

// validateClauseExpression validates an expression that belongs to clause, which
// must not call an aggregate function.
func validateClauseExpression(expression Expression, sources map[string]struct{}, clause string, path string) error {
	_, err := validateExpression(expression, clauseContext(sources, clause), path)
	return err
}

func validateExpression(expression Expression, ctx expressionContext, path string) (expressionUsage, error) {
	if expression == nil || (reflect.ValueOf(expression).Kind() == reflect.Ptr && reflect.ValueOf(expression).IsNil()) {
		return expressionUsage{}, validationError(path, "must not be nil")
	}

	switch expression := expression.(type) {
	case Column:
		if err := expression.source.validate(); err != nil {
			return expressionUsage{}, validationError(path, "%s", err)
		}
		if _, exists := ctx.sources[expression.source.key()]; !exists {
			return expressionUsage{}, validationError(path, "references table %q outside the statement", expression.source.Qualifier())
		}
		if _, exists := expression.source.definition.Column(expression.name); !exists {
			return expressionUsage{}, validationError(path, "references unknown column %q", expression.name)
		}
		return expressionUsage{bareColumn: ctx.aggregateDepth == 0}, nil
	case ExcludedColumn:
		if err := expression.column.source.validate(); err != nil {
			return expressionUsage{}, validationError(path, "%s", err)
		}
		if _, exists := ctx.sources[expression.column.source.key()]; !exists {
			return expressionUsage{}, validationError(path, "references table %q outside the statement", expression.column.source.Qualifier())
		}
		if _, exists := expression.column.source.definition.Column(expression.column.name); !exists {
			return expressionUsage{}, validationError(path, "references unknown column %q", expression.column.name)
		}
		return expressionUsage{bareColumn: ctx.aggregateDepth == 0}, nil
	case Value:
		return expressionUsage{}, nil
	case Binary:
		if !validBinaryOperator(expression.operator) {
			return expressionUsage{}, validationError(path+".operator", "unsupported operator %q", expression.operator)
		}
		left, err := validateExpression(expression.left, ctx, path+".left")
		if err != nil {
			return expressionUsage{}, err
		}
		right, err := validateExpression(expression.right, ctx, path+".right")
		if err != nil {
			return expressionUsage{}, err
		}
		return left.merge(right), nil
	case Logical:
		if expression.operator != LogicalAnd && expression.operator != LogicalOr {
			return expressionUsage{}, validationError(path+".operator", "unsupported operator %q", expression.operator)
		}
		if len(expression.expressions) < 2 {
			return expressionUsage{}, validationError(path, "requires at least two expressions")
		}
		var usage expressionUsage
		for i, child := range expression.expressions {
			childUsage, err := validateExpression(child, ctx, fmt.Sprintf("%s.expressions[%d]", path, i))
			if err != nil {
				return expressionUsage{}, err
			}
			usage = usage.merge(childUsage)
		}
		return usage, nil
	case Not:
		return validateExpression(expression.expr, ctx, path+".expression")
	case NullTest:
		return validateExpression(expression.expr, ctx, path+".expression")
	case Membership:
		// A membership test is an ordinary predicate, not an aggregate, so it
		// carries ctx through unchanged: the tested expression and every member of
		// the value list sit in the same clause and at the same aggregate depth as
		// the test itself. An aggregate hidden in either operand is therefore
		// judged by the rule that governs the clause, and a column read there
		// counts as a bare column exactly as it would outside the test.
		if len(expression.values) == 0 {
			return expressionUsage{}, validationError(path, "requires at least one value")
		}
		usage, err := validateExpression(expression.expr, ctx, path+".expression")
		if err != nil {
			return expressionUsage{}, err
		}
		for i, value := range expression.values {
			valueUsage, err := validateExpression(value, ctx, fmt.Sprintf("%s.values[%d]", path, i))
			if err != nil {
				return expressionUsage{}, err
			}
			usage = usage.merge(valueUsage)
		}
		return usage, nil
	case Function:
		return validateFunction(expression, ctx, path)
	default:
		return expressionUsage{}, validationError(path, "uses unsupported expression %T", expression)
	}
}

// validateFunction validates a function call and the placement rules that make
// it legal SQL. Every name validFunctionName accepts is an aggregate, so a call
// that reaches here is an aggregate call.
func validateFunction(function Function, ctx expressionContext, path string) (expressionUsage, error) {
	if !validFunctionName(function.name) {
		return expressionUsage{}, validationError(path+".function", "unsupported function %q", function.name)
	}
	if ctx.aggregateDepth > 0 {
		return expressionUsage{}, validationError(path, "calls aggregate function %q inside another aggregate function", function.name)
	}
	if !ctx.allowsAggregate {
		return expressionUsage{}, validationError(path, "calls aggregate function %q in %s, but an aggregate is only valid in a SELECT projection, or in an ORDER BY clause of a statement whose projections all aggregate", function.name, ctx.clause)
	}
	if function.star {
		if function.name != FunctionCount {
			return expressionUsage{}, validationError(path, "function %q does not support *", function.name)
		}
		if len(function.arguments) > 0 {
			return expressionUsage{}, validationError(path, "COUNT(*) takes no arguments")
		}
		return expressionUsage{aggregate: true}, nil
	}
	if len(function.arguments) != 1 {
		return expressionUsage{}, validationError(path, "function %q takes exactly one argument, got %d", function.name, len(function.arguments))
	}
	arguments := ctx.inAggregate()
	for i, argument := range function.arguments {
		// A column read inside an aggregate is aggregated, so the returned usage
		// reports the call itself and never the arguments it consumed.
		if _, err := validateExpression(argument, arguments, fmt.Sprintf("%s.arguments[%d]", path, i)); err != nil {
			return expressionUsage{}, err
		}
	}
	return expressionUsage{aggregate: true}, nil
}

func validBinaryOperator(operator BinaryOperator) bool {
	switch operator {
	case OperatorEqual, OperatorNotEqual, OperatorGreaterThan, OperatorGreaterThanOrEqual, OperatorLessThan, OperatorLessThanOrEqual, OperatorLike:
		return true
	default:
		return false
	}
}

// validFunctionName reports whether name is a supported function. Every name it
// accepts is an aggregate; a scalar function would need its own placement rules
// in validateFunction, which today treats each accepted call as an aggregate.
func validFunctionName(name FunctionName) bool {
	switch name {
	case FunctionCount, FunctionSum, FunctionMin, FunctionMax, FunctionAvg:
		return true
	default:
		return false
	}
}
