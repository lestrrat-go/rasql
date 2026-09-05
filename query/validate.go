package query

import (
	"fmt"
	"reflect"

	"github.com/lestrrat-go/rasql/internal/nilcheck"
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

// validateOrderResultAlias refuses an ORDER BY term built by AscResult or
// DescResult that PostgreSQL and MySQL would themselves refuse. projection
// must report a result name at all, that name must be a legal identifier, it
// must be reported by at least one projection of the statement, and it must
// not be reported by more than one. results is Select.resultNames, counting
// how many projections report each name; membership and ambiguity are judged
// by that resolved name rather than by comparing projection against the
// statement's projections with ==, since a projection built by Project can
// hold an expression whose dynamic type is not comparable.
func validateOrderResultAlias(projection Projection, results map[string]int, path string) error {
	if nilcheck.Is(projection) {
		return validationError(path, "must not be nil")
	}
	name, ok := ResultName(projection)
	if !ok {
		return validationError(path, "orders by a projection with no result name; give it one with As, or use Asc on the expression instead")
	}
	if err := validateAlias(name); err != nil {
		return validationError(path, "%s", err)
	}
	switch count := results[name]; count {
	case 0:
		return validationError(path, "orders by the result name %q, which no projection of this statement reports; add the projection to Project before ordering by it", name)
	case 1:
		return nil
	default:
		return validationError(path, "orders by the result name %q, which %d projections report, so the ordering is ambiguous; give one of them a distinct alias", name, count)
	}
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
func validateSourceReference(references []sourceReference, source TableRef, path string) error {
	candidate := source.reference()
	for _, existing := range references {
		if !existing.conflicts(candidate) {
			continue
		}
		return validationError(path, "table %q is referred to as %q, which already refers to table %q; give one of them a distinct alias", candidate.descriptor, candidate.qualifier, existing.descriptor)
	}
	return nil
}

// sourceScope is every table a statement's expressions may name: the tables the
// statement itself selects from, and, for a subquery, the tables of every
// statement enclosing it, so a correlated subquery can read the enclosing row.
//
// It carries two views of those tables, because neither can be derived from the
// other. keys states which descriptor a column belongs to, so a ColumnRef
// resolves in one map lookup. references states how a server would resolve a
// column reference written against a table, which is what tells apart two
// sources that hold different keys and still render under the same leading
// identifier; sourceReference.conflicts owns that comparison. Keeping the two
// in one value is what stops a caller from threading the lookup set into a
// subquery while leaving the ambiguity check behind.
type sourceScope struct {
	keys       map[string]struct{}
	references []sourceReference
	// correlates reports whether a subquery validated inside this scope
	// inherits it and may therefore read a column of it. A SELECT statement's
	// scope does: the subquery runs once per row the SELECT is on, and that row
	// is what the correlated column reads. A write statement's scope does not:
	// its target table is rows being written rather than rows being read, and a
	// DELETE or an UPDATE has no result row for a subquery to be evaluated
	// against. It is also what tells a top-level statement from a nested one,
	// since Select.Validate starts from the zero scope.
	correlates bool
}

// newSourceScope returns a scope holding table alone, and correlating with
// nothing. It is what a write statement validates against: the target table so
// its own clauses resolve a column, and no correlation, so a subquery in one of
// those clauses is still validated on its own.
func newSourceScope(table TableRef) sourceScope {
	scope := sourceScope{keys: make(map[string]struct{}, 1)}
	scope.add(table)
	return scope
}

// nested returns a scope for a statement enclosed by the one s describes. It
// copies both views, so the tables the inner statement goes on to add never
// reach back into the enclosing statement's own scope.
func (s sourceScope) nested() sourceScope {
	nested := sourceScope{keys: make(map[string]struct{}, len(s.keys)+1)}
	for key := range s.keys {
		nested.keys[key] = struct{}{}
	}
	nested.references = append(nested.references, s.references...)
	return nested
}

// add records table as a source in scope. Call it only after
// validateSourceReference has cleared table against the sources already there,
// since the check reads exactly the references this appends to.
func (s *sourceScope) add(table TableRef) {
	s.keys[table.key()] = struct{}{}
	s.references = append(s.references, table.reference())
}

// expressionContext tells the expression walk where in a statement the
// expression sits. An aggregate function is only legal in a SELECT projection,
// in a HAVING clause, and in the ORDER BY of a statement that groups, and never
// inside another aggregate, so a walk that carries no clause and no nesting
// state cannot tell a legal call from one every dialect rejects.
type expressionContext struct {
	sources sourceScope
	// clause names the SQL clause the expression belongs to, for error messages.
	clause string
	// allowsAggregate reports whether the clause may call an aggregate at all.
	allowsAggregate bool
	// allowsSubquery reports whether the clause may run a subquery at all.
	allowsSubquery bool
	// aggregateDepth counts the aggregate calls the walk is currently inside.
	aggregateDepth int
	// rowValue reports whether the expression sits in a VALUES row of an
	// INSERT statement. Such a row is evaluated before any row exists to read
	// from, so a ColumnRef naming a source this context already carries (the
	// INSERT's own target table) is refused with a message of its own rather
	// than the generic "outside the statement" one, which would otherwise stay
	// silent about the real problem: PostgreSQL and SQLite refuse the
	// reference outright, and MySQL accepts it but resolves it to whatever row
	// already exists, silently writing the wrong data.
	rowValue bool
}

// clauseContext returns a context for a clause that must not call an aggregate.
func clauseContext(sources sourceScope, clause string) expressionContext {
	return expressionContext{sources: sources, clause: clause}
}

// rowValueContext returns a context for a VALUES row of an INSERT statement.
// sources still carries the target table so a reference to another table
// keeps reporting the "outside the statement" error, but a reference to the
// target table itself is refused with rowValue's dedicated message instead.
func rowValueContext(sources sourceScope, clause string) expressionContext {
	return expressionContext{sources: sources, clause: clause, rowValue: true}
}

// aggregateClauseContext returns a context for a clause that may call an
// aggregate. It permits the call itself; a caller that also has to refuse a
// column read outside every aggregate reads bareColumn from the returned usage.
// It also allows a subquery, since every clause this context serves belongs to a
// SELECT statement.
func aggregateClauseContext(sources sourceScope, clause string) expressionContext {
	return expressionContext{sources: sources, clause: clause, allowsAggregate: true, allowsSubquery: true}
}

// subqueryClauseContext returns a context for a clause that must not call an
// aggregate but may run a subquery. It serves every SELECT clause that is not
// itself an aggregate clause — a JOIN ON condition, WHERE, GROUP BY, and ORDER
// BY — the WHERE clause of a DELETE, and the WHERE clause and SET assignment
// values of an UPDATE.
//
// The write clauses left out are INSERT's VALUES rows, an upsert's
// conflict-update assignments, and every RETURNING projection. Each stays on
// validateClauseExpression until someone confirms against a live server what
// the three engines do with a subquery there, the way this repository
// confirmed DELETE's and UPDATE's. Extending the list is this comment, the
// clause's own call swapped for validateSubqueryClauseExpression, and the
// clause named in the refusal message validateExpression's Subquery arm
// builds.
func subqueryClauseContext(sources sourceScope, clause string) expressionContext {
	return expressionContext{sources: sources, clause: clause, allowsSubquery: true}
}

// projectionContext returns a context for a SELECT projection.
func projectionContext(sources sourceScope) expressionContext {
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
func validateClauseExpression(expression Expression, sources sourceScope, clause string, path string) error {
	_, err := validateExpression(expression, clauseContext(sources, clause), path)
	return err
}

// validateSubqueryClauseExpression validates an expression that belongs to a
// clause which must not call an aggregate function but may run a subquery. See
// subqueryClauseContext for which clauses those are.
func validateSubqueryClauseExpression(expression Expression, sources sourceScope, clause string, path string) error {
	_, err := validateExpression(expression, subqueryClauseContext(sources, clause), path)
	return err
}

// validateRowValueExpression validates an expression that belongs to a VALUES
// row of an INSERT statement: it must not call an aggregate, must not run a
// subquery, and must not read a column of the row's own target table, because
// no row exists yet for such a read to resolve against.
func validateRowValueExpression(expression Expression, sources sourceScope, clause string, path string) error {
	_, err := validateExpression(expression, rowValueContext(sources, clause), path)
	return err
}

func validateExpression(expression Expression, ctx expressionContext, path string) (expressionUsage, error) {
	if expression == nil || (reflect.ValueOf(expression).Kind() == reflect.Pointer && reflect.ValueOf(expression).IsNil()) {
		return expressionUsage{}, validationError(path, "must not be nil")
	}

	switch expression := expression.(type) {
	case ColumnRef:
		if err := expression.source.validate(); err != nil {
			return expressionUsage{}, validationError(path, "%s", err)
		}
		_, inSources := ctx.sources.keys[expression.source.key()]
		if ctx.rowValue && inSources {
			return expressionUsage{}, validationError(path, "references column %q of the target table, but an INSERT VALUES row cannot read the target table's columns", expression.name)
		}
		if !inSources {
			return expressionUsage{}, validationError(path, "references table %q outside the statement", expression.source.QualifiedName())
		}
		if _, exists := expression.source.column(expression.name); !exists {
			return expressionUsage{}, validationError(path, "references unknown column %q", expression.name)
		}
		return expressionUsage{bareColumn: ctx.aggregateDepth == 0}, nil
	case ExcludedColumn:
		if err := expression.column.source.validate(); err != nil {
			return expressionUsage{}, validationError(path, "%s", err)
		}
		if _, exists := ctx.sources.keys[expression.column.source.key()]; !exists {
			return expressionUsage{}, validationError(path, "references table %q outside the statement", expression.column.source.QualifiedName())
		}
		if _, exists := expression.column.source.column(expression.column.name); !exists {
			return expressionUsage{}, validationError(path, "references unknown column %q", expression.column.name)
		}
		return expressionUsage{bareColumn: ctx.aggregateDepth == 0}, nil
	case TableIdentifier:
		if err := expression.table.validate(); err != nil {
			return expressionUsage{}, validationError(path, "%s", err)
		}
		if _, inSources := ctx.sources.keys[expression.table.key()]; !inSources {
			return expressionUsage{}, validationError(path, "references table %q outside the statement", expression.table.QualifiedName())
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
		if err := validateSubqueryStatement(expression.statement, ctx, path); err != nil {
			return expressionUsage{}, err
		}
		if n := len(expression.statement.projections); n != 1 {
			return expressionUsage{}, validationError(path, "a subquery used as an expression must select exactly one expression, got %d", n)
		}
		return subqueryUsage(), nil
	case Existence:
		// EXISTS reads whether a row arrived and never reads a value, so the
		// projection count Scalar, InSelect and NotInSelect require does not
		// apply here, and the statement may project anything at all. Existence
		// states why that difference makes it a node of its own, and Exists
		// states which body is portable.
		if err := validateSubqueryStatement(expression.subquery.statement, ctx, path+".subquery"); err != nil {
			return expressionUsage{}, err
		}
		return subqueryUsage(), nil
	default:
		return expressionUsage{}, validationError(path, "uses unsupported expression %T", expression)
	}
}

// validateSubqueryStatement validates the SELECT a subquery runs, in the scope
// of the statement that encloses it. Both subquery forms go through it, so the
// clause rule and the scope rule are stated once; each caller adds only what its
// own form demands of the projections.
//
// The statement is validated through Select.validate rather than the exported
// Select.Validate, because a statement validated on its own knows nothing of the
// tables enclosing it and would refuse every correlated column reference as
// naming a table outside the statement. ctx.sources is the enclosing statement's
// own scope, already the union of its tables and those of anything enclosing
// it, so passing it here accumulates correlation to any nesting depth.
//
// A write statement's scope does not correlate, so a subquery in a DELETE's or
// an UPDATE's clause is validated on its own, exactly as it was before
// correlation existed. That keeps the shape those clauses were opened for:
// DELETE FROM users WHERE users.id IN (SELECT users.id FROM users …) reads the
// target table in the subquery's own FROM, which render refuses for MySQL alone
// because of its error 1093, and which merging the target into the subquery's
// scope would instead refuse for every dialect as an ambiguous source. A
// statement that declared a correlation is refused there rather than rendered,
// since nothing has checked what the three engines do with a correlated
// subquery in a write clause.
func validateSubqueryStatement(statement Select, ctx expressionContext, path string) error {
	if !ctx.allowsSubquery {
		return validationError(path, "runs a subquery in %s, but a subquery is only valid in the projections, JOIN ON conditions, WHERE clause, GROUP BY clause, HAVING clause, and ORDER BY clause of a SELECT statement, in the WHERE clause of a DELETE statement, and in the WHERE clause and SET assignments of an UPDATE statement", ctx.clause)
	}
	outer := ctx.sources
	if !outer.correlates {
		if len(statement.correlations) > 0 {
			return validationError(path, "runs a correlated subquery in %s, but a subquery correlates only with a SELECT statement, which has a result row for it to read", ctx.clause)
		}
		// The write target stays out of the subquery's scope entirely, so the
		// subquery is judged exactly as it was while it was being built.
		outer = sourceScope{}
	}
	if err := statement.validate(outer); err != nil {
		return validationError(path+".statement", "%s", err)
	}
	return nil
}

// subqueryUsage is what both subquery forms report back to the statement that
// encloses them: nothing at all.
//
// aggregate stays false because an aggregate call inside a subquery belongs to
// that subquery's own clause and its own grouping. SELECT users.id, (SELECT
// AVG(orders.total) FROM orders) FROM users aggregates nothing at the outer
// level, and reporting the inner AVG outward would make the outer projection
// set look like it mixes an aggregate with a bare column and refuse a statement
// every dialect runs. The subquery's own validation has already judged that call
// against its own clause, in a context built inside Select.validate that starts
// again at aggregate depth zero -- which is also why an aggregate is legal
// inside a subquery that sits inside an aggregate's argument, since the subquery
// breaks the nesting SQL forbids.
//
// bareColumn stays false for the same reason. The columns a subquery reads
// belong to the subquery's rows, not to the enclosing statement's, so reporting
// them outward would make an ungrouped enclosing statement look like it reads a
// bare column beside an aggregate and refuse it. A correlated reference to an
// enclosing table is the one read that does belong to the enclosing row, and
// this leaves it to the database rather than checking it here, exactly as
// Select.validateProjectionSet already leaves "a column projected outside an
// aggregate is not among the grouping keys" to the database: naming the shapes a
// server refuses there needs primary-key and outer-join reasoning this package
// does not have.
func subqueryUsage() expressionUsage {
	return expressionUsage{}
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
	if function.name == FunctionBM25 {
		return validateBM25Function(function, ctx, path)
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

// validateBM25Function validates a call to FunctionBM25. It is scalar, like
// validateScalarFunction, and follows the same placement rule with no
// DISTINCT and no *; it departs from validateScalarFunction only in its
// arity, which is variable, and in checking the shape of its first argument,
// which every other curated function leaves to whatever expression node the
// caller passed. bm25's first argument must be a TableIdentifier: a
// ColumnRef or a bound value there is not a mistake SQL itself would catch,
// since either still reaches the database as a plausible-looking argument,
// so this is exactly the shape the SQL text cannot check for us.
func validateBM25Function(function Function, ctx expressionContext, path string) (expressionUsage, error) {
	if function.star {
		return expressionUsage{}, validationError(path, "function %q does not support *", function.name)
	}
	if function.distinct {
		return expressionUsage{}, validationError(path, "function %q does not aggregate, so it does not support DISTINCT", function.name)
	}
	if len(function.arguments) == 0 {
		return expressionUsage{}, validationError(path, "function %q takes the table's own identifier as its first argument, followed by zero or more column weights", function.name)
	}
	if _, ok := function.arguments[0].(TableIdentifier); !ok {
		return expressionUsage{}, validationError(fmt.Sprintf("%s.arguments[0]", path), "function %q takes a TableIdentifier as its first argument, got %T; build one with TableRef.Identifier", function.name, function.arguments[0])
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
	case OperatorEqual, OperatorNotEqual, OperatorGreaterThan, OperatorGreaterThanOrEqual, OperatorLessThan, OperatorLessThanOrEqual, OperatorLike, OperatorMatch:
		return true
	default:
		return false
	}
}
