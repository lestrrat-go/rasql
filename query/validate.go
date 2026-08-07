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

// validateSourceReference refuses a source a server could not tell apart from
// one the statement already carries, and it is a separate check from the table
// key on purpose. The key states which descriptor a column belongs to, and two
// sources that differ by schema, by name or by alias hold different keys while
// still rendering column references a server resolves to both of them.
//
// The check refuses the statement rather than inventing an alias to separate the
// two sources. An alias rasql chose would change the SQL the caller asked for
// and, through a projected column's result name, what a decoded row looks like,
// and rasql cannot tell which of the two sources any already-written column
// reference meant. Refusing names both tables and leaves the caller to pick an
// alias with As, which is the one repair that keeps the statement theirs.
func validateSourceReference(references []sourceReference, source Table, path string) error {
	candidate := source.reference()
	for _, existing := range references {
		if !existing.conflicts(candidate) {
			continue
		}
		return validationError(path, "table %q is referred to as %q, which already refers to table %q; give one of them a distinct alias", candidate.descriptor, candidate.qualifier, existing.descriptor)
	}
	return nil
}

// expressionContext tells the expression walk where in a statement the
// expression sits. An aggregate function is only legal in a SELECT projection,
// in a HAVING clause, and in the ORDER BY of a statement that groups, and never
// inside another aggregate, so a walk that carries no clause and no nesting
// state cannot tell a legal call from one every dialect rejects.
type expressionContext struct {
	sources map[string]struct{}
	// clause names the SQL clause the expression belongs to, for error messages.
	clause string
	// allowsAggregate reports whether the clause may call an aggregate at all.
	allowsAggregate bool
	// allowsSubquery reports whether the clause may run a subquery at all.
	allowsSubquery bool
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
// It also allows a subquery, since every clause this context serves belongs to a
// SELECT statement.
func aggregateClauseContext(sources map[string]struct{}, clause string) expressionContext {
	return expressionContext{sources: sources, clause: clause, allowsAggregate: true, allowsSubquery: true}
}

// selectClauseContext returns a context for a clause of a SELECT statement that
// must not call an aggregate but may run a subquery.
func selectClauseContext(sources map[string]struct{}, clause string) expressionContext {
	return expressionContext{sources: sources, clause: clause, allowsSubquery: true}
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
	// aggregate call, which an ungrouped statement must not combine with an
	// aggregate because no supported dialect answers that combination usefully.
	// A statement with a GROUP BY may combine the two, so its callers ignore
	// this.
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

// validateSelectClauseExpression validates an expression that belongs to clause
// of a SELECT statement, which must not call an aggregate function but may run a
// subquery.
func validateSelectClauseExpression(expression Expression, sources map[string]struct{}, clause string, path string) error {
	_, err := validateExpression(expression, selectClauseContext(sources, clause), path)
	return err
}

func validateExpression(expression Expression, ctx expressionContext, path string) (expressionUsage, error) {
	if expression == nil || (reflect.ValueOf(expression).Kind() == reflect.Pointer && reflect.ValueOf(expression).IsNil()) {
		return expressionUsage{}, validationError(path, "must not be nil")
	}

	switch expression := expression.(type) {
	case Column:
		if err := expression.source.validate(); err != nil {
			return expressionUsage{}, validationError(path, "%s", err)
		}
		if _, exists := ctx.sources[expression.source.key()]; !exists {
			return expressionUsage{}, validationError(path, "references table %q outside the statement", expression.source.QualifiedName())
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
			return expressionUsage{}, validationError(path, "references table %q outside the statement", expression.column.source.QualifiedName())
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
		// the value list (or the subquery) sit in the same clause and at the same
		// aggregate depth as the test itself. An aggregate hidden in either operand
		// is therefore judged by the rule that governs the clause, and a column
		// read there counts as a bare column exactly as it would outside the test.
		if expression.hasSubquery {
			usage, err := validateExpression(expression.expr, ctx, path+".expression")
			if err != nil {
				return expressionUsage{}, err
			}
			subqueryUsage, err := validateExpression(expression.subquery, ctx, path+".subquery")
			if err != nil {
				return expressionUsage{}, err
			}
			return usage.merge(subqueryUsage), nil
		}
		if len(expression.values) == 0 {
			return expressionUsage{}, validationError(path, "requires at least one value")
		}
		usage, err := validateExpression(expression.expr, ctx, path+".expression")
		if err != nil {
			return expressionUsage{}, err
		}
		for i, value := range expression.values {
			itemPath := fmt.Sprintf("%s.values[%d]", path, i)
			if _, ok := value.(Subquery); ok {
				return expressionUsage{}, validationError(itemPath, "puts a subquery in an IN value list; use InSelect or NotInSelect for IN (SELECT …)")
			}
			valueUsage, err := validateExpression(value, ctx, itemPath)
			if err != nil {
				return expressionUsage{}, err
			}
			usage = usage.merge(valueUsage)
		}
		return usage, nil
	case Function:
		return validateFunction(expression, ctx, path)
	case Subquery:
		if !ctx.allowsSubquery {
			return expressionUsage{}, validationError(path, "runs a subquery in %s, but a subquery is only valid in the projections, JOIN ON conditions, WHERE clause, GROUP BY clause, HAVING clause, and ORDER BY clause of a SELECT statement", ctx.clause)
		}
		if err := expression.statement.Validate(); err != nil {
			return expressionUsage{}, validationError(path+".statement", "%s", err)
		}
		if n := len(expression.statement.projections); n != 1 {
			return expressionUsage{}, validationError(path, "a subquery used as an expression must select exactly one expression, got %d", n)
		}
		return expressionUsage{}, nil
	default:
		return expressionUsage{}, validationError(path, "uses unsupported expression %T", expression)
	}
}

// functionSpec states how many arguments a supported function takes and
// whether the call aggregates. A maximum of 0 means the function takes any
// number of arguments at or above the minimum.
type functionSpec struct {
	aggregate bool
	minimum   int
	maximum   int
}

// functionSpecs is the curated set of function names Call accepts, each with
// its arity and whether it aggregates. Func bypasses this table entirely: it
// is the escape hatch for a function name this package does not curate, and
// validateCustomFunction checks only that its name is a legal identifier.
var functionSpecs = map[FunctionName]functionSpec{
	FunctionCount: {aggregate: true, minimum: 1, maximum: 1},
	FunctionSum:   {aggregate: true, minimum: 1, maximum: 1},
	FunctionMin:   {aggregate: true, minimum: 1, maximum: 1},
	FunctionMax:   {aggregate: true, minimum: 1, maximum: 1},
	FunctionAvg:   {aggregate: true, minimum: 1, maximum: 1},

	FunctionCoalesce: {minimum: 2},
	FunctionLower:    {minimum: 1, maximum: 1},
	FunctionUpper:    {minimum: 1, maximum: 1},
	FunctionAbs:      {minimum: 1, maximum: 1},
}

// validateFunction validates a function call and the placement rules that
// make it legal SQL. A call built with Func skips the curated name table
// entirely and follows the scalar placement rule with no arity check, because
// rasql knows nothing about an escape-hatch function beyond its name.
// Otherwise the name must be one of functionSpecs, which states whether the
// call aggregates.
func validateFunction(function Function, ctx expressionContext, path string) (expressionUsage, error) {
	if function.unchecked {
		return validateCustomFunction(function, ctx, path)
	}
	spec, ok := functionSpecs[function.name]
	if !ok {
		return expressionUsage{}, validationError(path+".function", "unsupported function %q", function.name)
	}
	if spec.aggregate {
		return validateAggregateFunction(function, ctx, path)
	}
	return validateScalarFunction(function, spec, ctx, path)
}

// validateAggregateFunction validates a call to one of the curated aggregate
// names: it is only legal in a SELECT projection, in a HAVING clause, or in
// the ORDER BY clause of a statement that groups, never inside another
// aggregate, and its arguments are walked one level deeper into aggregate
// nesting so a nested aggregate call is refused.
func validateAggregateFunction(function Function, ctx expressionContext, path string) (expressionUsage, error) {
	if ctx.aggregateDepth > 0 {
		return expressionUsage{}, validationError(path, "calls aggregate function %q inside another aggregate function", function.name)
	}
	if !ctx.allowsAggregate {
		return expressionUsage{}, validationError(path, "calls aggregate function %q in %s, but an aggregate is only valid in a SELECT projection, in a HAVING clause, or in the ORDER BY clause of a statement that groups", function.name, ctx.clause)
	}
	if function.star {
		if function.name != FunctionCount {
			return expressionUsage{}, validationError(path, "function %q does not support *", function.name)
		}
		if len(function.arguments) > 0 {
			return expressionUsage{}, validationError(path, "COUNT(*) takes no arguments")
		}
		if function.distinct {
			return expressionUsage{}, validationError(path, "COUNT(DISTINCT *) is not valid SQL; call Count with a column instead of CountAll")
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

// validateScalarFunction validates a call to one of the curated scalar names:
// COALESCE, LOWER, UPPER, or ABS. Unlike an aggregate call, ctx carries
// through to every argument unchanged, so a scalar call is legal wherever any
// expression is and its arguments are judged exactly as if they appeared in
// the call's place directly — including an aggregate nested inside one, which
// validateAggregateFunction still judges by ctx.allowsAggregate. WithDistinct
// is refused here: DISTINCT inside a call asks the function to combine one row
// per distinct argument value, which only an aggregate does, so LOWER(DISTINCT
// x) is a syntax error on every supported dialect.
func validateScalarFunction(function Function, spec functionSpec, ctx expressionContext, path string) (expressionUsage, error) {
	if function.star {
		return expressionUsage{}, validationError(path, "function %q does not support *", function.name)
	}
	if function.distinct {
		return expressionUsage{}, validationError(path, "function %q does not aggregate, so it does not support DISTINCT", function.name)
	}
	if err := validateFunctionArity(function.name, len(function.arguments), spec, path); err != nil {
		return expressionUsage{}, err
	}
	var usage expressionUsage
	for i, argument := range function.arguments {
		argumentUsage, err := validateExpression(argument, ctx, fmt.Sprintf("%s.arguments[%d]", path, i))
		if err != nil {
			return expressionUsage{}, err
		}
		usage = usage.merge(argumentUsage)
	}
	return usage, nil
}

// validateCustomFunction validates a call built with Func: the escape hatch
// that accepts any function name. Validation checks only that name is a
// legal SQL identifier, reusing schema.ValidateIdentifier rather than
// duplicating that rule, and reports a clear error naming the offending
// input when it is not. The call is always treated as scalar, exactly like
// validateScalarFunction, with no arity check, because rasql has no arity
// table for a function it does not curate. WithDistinct is the one place the
// two part company: validateScalarFunction refuses it, while a Func call
// carries it through to the rendered SQL, because rasql does not know whether
// the named function aggregates and DISTINCT is the only way to reach an
// aggregate it does not curate, such as GROUP_CONCAT. A DISTINCT the target
// database refuses fails there, like every other property of a Func name.
func validateCustomFunction(function Function, ctx expressionContext, path string) (expressionUsage, error) {
	if function.star {
		return expressionUsage{}, validationError(path, "function %q does not support *", function.name)
	}
	if err := schema.ValidateIdentifier(string(function.name)); err != nil {
		return expressionUsage{}, validationError(path+".function", "invalid function name %q: %s", function.name, err)
	}
	var usage expressionUsage
	for i, argument := range function.arguments {
		argumentUsage, err := validateExpression(argument, ctx, fmt.Sprintf("%s.arguments[%d]", path, i))
		if err != nil {
			return expressionUsage{}, err
		}
		usage = usage.merge(argumentUsage)
	}
	return usage, nil
}

// validateFunctionArity checks count against the arguments spec allows,
// reporting an exact-count message when the spec fixes one and an
// at-least message when it does not.
func validateFunctionArity(name FunctionName, count int, spec functionSpec, path string) error {
	if spec.minimum == spec.maximum {
		if count != spec.minimum {
			return validationError(path, "function %q takes exactly %d argument(s), got %d", name, spec.minimum, count)
		}
		return nil
	}
	if count < spec.minimum {
		return validationError(path, "function %q takes at least %d argument(s), got %d", name, spec.minimum, count)
	}
	if spec.maximum > 0 && count > spec.maximum {
		return validationError(path, "function %q takes at most %d argument(s), got %d", name, spec.maximum, count)
	}
	return nil
}

func validBinaryOperator(operator BinaryOperator) bool {
	switch operator {
	case OperatorEqual, OperatorNotEqual, OperatorGreaterThan, OperatorGreaterThanOrEqual, OperatorLessThan, OperatorLessThanOrEqual, OperatorLike:
		return true
	default:
		return false
	}
}
