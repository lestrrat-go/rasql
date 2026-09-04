package query

import "fmt"

// Expression is a dialect-neutral SQL expression.
type Expression interface {
	expression()
}

// ColumnRef is a typed reference to a table column.
type ColumnRef struct {
	source TableRef
	name   string
}

func (ColumnRef) expression() {}

// Validate reports whether c names a column its source table holds.
//
// A statement runs this check itself, so a ColumnRef built from a name written
// into Go source needs no call here. It exists for a caller that takes a column
// name while the program runs and wants the bad name reported at the call that
// supplied it rather than at the statement that carries it.
func (c ColumnRef) Validate() error {
	if err := c.source.validate(); err != nil {
		return fmt.Errorf("query column: %q: %w", c.name, err)
	}
	if _, ok := c.source.column(c.name); !ok {
		return fmt.Errorf("query column: table %q has no column %q", c.source.QualifiedName(), c.name)
	}
	return nil
}

// Name returns the column name.
func (c ColumnRef) Name() string {
	return c.name
}

// Source returns the table that owns the column.
func (c ColumnRef) Source() TableRef {
	return c.source
}

// ProjectedExpression returns c, so a column is selected without a wrapper.
func (c ColumnRef) ProjectedExpression() Expression {
	return c
}

// ResultAlias returns an empty string, because an unaliased column is reported
// under its own name.
func (ColumnRef) ResultAlias() string {
	return ""
}

// As returns the column projected under alias. It reports no error, because
// the alias is checked when the statement carrying the projection is
// validated, exactly as the alias given to ExpressionProjection.As is.
// TableRef.As is a different operation: it names the table itself in the FROM
// clause, and it validates the alias immediately.
func (c ColumnRef) As(alias string) Projection {
	return ExpressionProjection{expression: c, alias: alias}
}

// ExcludedColumn references the incoming value of a column during an upsert.
type ExcludedColumn struct {
	column ColumnRef
}

func (ExcludedColumn) expression() {}

// Excluded references the incoming value for column in an upsert assignment.
func Excluded(column ColumnRef) ExcludedColumn {
	return ExcludedColumn{column: column}
}

// Column returns the incoming column.
func (c ExcludedColumn) Column() ColumnRef {
	return c.column
}

// TableIdentifier is an expression that renders a TableRef as a bare quoted
// identifier: no schema qualifier, no column, and no text beyond what
// Dialect.QuoteIdentifier returns for a table this statement already
// carries. It exists for the one kind of SQL that treats a table's own name
// as if it were a column: SQLite's FTS5 module reads the table name this way
// as the left operand of MATCH and as the first argument to bm25 and its
// other auxiliary functions. TableRef.Identifier builds it; nothing in this
// package builds one from a plain string, so it cannot become a way to place
// arbitrary text in a rendered statement.
type TableIdentifier struct {
	table TableRef
}

func (TableIdentifier) expression() {}

// Table returns the table whose bare name is rendered.
func (t TableIdentifier) Table() TableRef {
	return t.table
}

// Value is a bound SQL argument. Its value is never interpolated into SQL text.
type Value struct {
	value any
}

func (Value) expression() {}

// Bind creates a bound SQL argument expression. A comparison, a membership
// test, Call, Func, and Coalesce bind a plain Go value automatically, so most
// callers never write Bind directly; reach for it explicitly for a slot that
// still takes an Expression — Project, an And/Or operand, an Asc/Desc
// ordering key, or a GROUP BY key — and to say "treat this as data" about a
// value that happens to satisfy Expression itself. Bind(nil) is a bound NULL.
func Bind(value any) Value {
	return Value{value: value}
}

// Argument returns the value supplied to Bind.
func (v Value) Argument() any {
	return v.value
}

// operand returns value as an expression. An Expression built by this package
// is used as it stands; anything else is bound as a parameter, so a caller
// writes Equal(column, "ada@example.com") instead of
// Equal(column, Bind("ada@example.com")). A value that is already an
// Expression is never wrapped a second time.
func operand(value any) Expression {
	if expression, ok := value.(Expression); ok {
		return expression
	}
	return Bind(value)
}

// operands is operand over a variadic or slice argument. It always returns a
// new slice, so a caller's later write to values cannot reach inside a built
// expression.
func operands(values []any) []Expression {
	converted := make([]Expression, len(values))
	for i, value := range values {
		converted[i] = operand(value)
	}
	return converted
}

// BinaryOperator is an operator with a left and right expression.
type BinaryOperator string

const (
	OperatorEqual              BinaryOperator = "="
	OperatorNotEqual           BinaryOperator = "<>"
	OperatorGreaterThan        BinaryOperator = ">"
	OperatorGreaterThanOrEqual BinaryOperator = ">="
	OperatorLessThan           BinaryOperator = "<"
	OperatorLessThanOrEqual    BinaryOperator = "<="
	OperatorLike               BinaryOperator = "LIKE"
	// OperatorMatch is SQLite's full-text search operator, answered by a
	// virtual table's own module — FTS5 among them — rather than by SQL
	// itself. PostgreSQL and MySQL have no such operator, so render.Select
	// and render.Update refuse it unless the target dialect grants
	// dialect.CapabilityMatchOperator, which only dialect.SQLite does. Match
	// builds the common shape directly; Compare accepts this constant too,
	// for the less common shape that tests one column rather than the whole
	// table.
	OperatorMatch BinaryOperator = "MATCH"
)

// Binary combines two expressions with an operator.
type Binary struct {
	left     Expression
	operator BinaryOperator
	right    Expression
}

func (Binary) expression() {}

// Compare combines left and right with operator. Either operand may be a
// plain Go value, which is bound the way Bind would bind it; an operand that
// is already an Expression is used as it stands. nil binds as NULL, so
// Compare(left, OperatorEqual, nil) never matches a row — use IsNull to test
// for NULL.
func Compare(left any, operator BinaryOperator, right any) Binary {
	return Binary{left: operand(left), operator: operator, right: operand(right)}
}

// Equal compares left and right for equality. Either operand may be a plain
// Go value, which is bound; nil binds as NULL, so IsNull is what tests for
// NULL.
func Equal(left any, right any) Binary {
	return Compare(left, OperatorEqual, right)
}

// NotEqual compares left and right for inequality. Either operand may be a
// plain Go value, which is bound; nil binds as NULL, so IsNull is what tests
// for NULL.
func NotEqual(left any, right any) Binary {
	return Compare(left, OperatorNotEqual, right)
}

// GreaterThan compares left and right. Either operand may be a plain Go
// value, which is bound; nil binds as NULL, so IsNull is what tests for
// NULL.
func GreaterThan(left any, right any) Binary {
	return Compare(left, OperatorGreaterThan, right)
}

// GreaterThanOrEqual compares left and right. Either operand may be a plain
// Go value, which is bound; nil binds as NULL, so IsNull is what tests for
// NULL.
func GreaterThanOrEqual(left any, right any) Binary {
	return Compare(left, OperatorGreaterThanOrEqual, right)
}

// LessThan compares left and right. Either operand may be a plain Go value,
// which is bound; nil binds as NULL, so IsNull is what tests for NULL.
func LessThan(left any, right any) Binary {
	return Compare(left, OperatorLessThan, right)
}

// LessThanOrEqual compares left and right. Either operand may be a plain Go
// value, which is bound; nil binds as NULL, so IsNull is what tests for
// NULL.
func LessThanOrEqual(left any, right any) Binary {
	return Compare(left, OperatorLessThanOrEqual, right)
}

// Like compares left and right with SQL LIKE. Either operand may be a plain
// Go value, which is bound; nil binds as NULL, so IsNull is what tests for
// NULL.
func Like(left any, right any) Binary {
	return Compare(left, OperatorLike, right)
}

// Match tests table against a full-text query, rendering table's own bare
// identifier — never one of its columns — as the left operand of MATCH. This
// is the common SQLite FTS5 shape: the table name stands for every indexed
// column of the row at once, which is what lets one MATCH filter a whole
// row rather than a single column. expr may be a plain Go value, bound the
// way Compare binds it, or an Expression such as a bound query string built
// some other way.
//
// A caller who wants MATCH against one column instead, which FTS5 also
// supports, writes Compare(table.Column(name), OperatorMatch, expr)
// directly; Match only builds the whole-table shape, since that is the one
// bm25 and the target query in the rasql documentation both need.
func Match(table TableRef, expr any) Binary {
	return Binary{left: TableIdentifier{table: table}, operator: OperatorMatch, right: operand(expr)}
}

// Left returns the left expression.
func (b Binary) Left() Expression {
	return b.left
}

// Operator returns the binary operator.
func (b Binary) Operator() BinaryOperator {
	return b.operator
}

// Right returns the right expression.
func (b Binary) Right() Expression {
	return b.right
}

// LogicalOperator combines multiple boolean expressions.
type LogicalOperator string

const (
	LogicalAnd LogicalOperator = "AND"
	LogicalOr  LogicalOperator = "OR"
)

// Logical combines multiple expressions with AND or OR.
type Logical struct {
	operator    LogicalOperator
	expressions []Expression
}

func (Logical) expression() {}

// And combines expressions with AND.
func And(expressions ...Expression) Logical {
	return Logical{operator: LogicalAnd, expressions: append([]Expression(nil), expressions...)}
}

// Or combines expressions with OR.
func Or(expressions ...Expression) Logical {
	return Logical{operator: LogicalOr, expressions: append([]Expression(nil), expressions...)}
}

// Operator returns the logical operator.
func (l Logical) Operator() LogicalOperator {
	return l.operator
}

// Expressions returns a copy of the expressions.
func (l Logical) Expressions() []Expression {
	return append([]Expression(nil), l.expressions...)
}

// Not negates an expression.
type Not struct {
	expr Expression
}

func (Not) expression() {}

// Negate creates a NOT expression.
func Negate(expression Expression) Not {
	return Not{expr: expression}
}

// Expression returns the expression being negated.
func (n Not) Expression() Expression {
	return n.expr
}

// NullTest checks whether an expression is NULL.
type NullTest struct {
	expr Expression
	not  bool
}

func (NullTest) expression() {}

// IsNull tests whether expression is NULL.
func IsNull(expression Expression) NullTest {
	return NullTest{expr: expression}
}

// IsNotNull tests whether expression is not NULL.
func IsNotNull(expression Expression) NullTest {
	return NullTest{expr: expression, not: true}
}

// Expression returns the expression being tested.
func (n NullTest) Expression() Expression {
	return n.expr
}

// Not reports whether the test is IS NOT NULL.
func (n NullTest) Not() bool {
	return n.not
}

// Membership tests whether an expression matches one of a list of values.
type Membership struct {
	expr        Expression
	values      []Expression
	not         bool
	subquery    Subquery
	hasSubquery bool
}

func (Membership) expression() {}

// In tests whether expression equals one of values.
// It renders as expression IN (…). Each argument that is not itself an
// Expression is bound and costs one parameter, the same as passing it to
// Bind; a column argument renders as a quoted identifier and costs none. A
// slice argument is bound whole as a single parameter and is never expanded
// — convert a []T to individual arguments element by element when each
// element should be its own placeholder. Statement validation rejects an
// empty value list, because IN () is not valid SQL in any supported dialect,
// and rejects a Subquery placed among the values; use InSelect for
// expression IN (SELECT …), which also fits a large set within the dialect's
// parameter limit.
func In(expression any, values ...any) Membership {
	return Membership{expr: operand(expression), values: operands(values)}
}

// NotIn tests whether expression differs from every one of values.
// It renders as expression NOT IN (…) and follows the same argument-binding
// rules as In, including the rejection of an empty value list and of a
// Subquery among the values by statement validation; use NotInSelect for
// expression NOT IN (SELECT …). A NULL among values never makes the test true:
// it is false when a non-NULL value equals expression, and unknown otherwise. A
// WHERE clause keeps neither, so a NOT IN over a list that may contain NULL
// matches no rows at all.
func NotIn(expression any, values ...any) Membership {
	return Membership{expr: operand(expression), values: operands(values), not: true}
}

// InSelect tests whether expression equals a value the statement returns.
// It renders as expression IN (SELECT …). The statement must project exactly one
// expression, which statement validation checks. Unlike In, it costs no argument
// per candidate value, so a set of any size fits within the dialect's parameter
// limit.
func InSelect(expression any, statement Select) Membership {
	return Membership{expr: operand(expression), subquery: Scalar(statement), hasSubquery: true}
}

// NotInSelect tests whether expression differs from every value the statement
// returns. It renders as expression NOT IN (SELECT …) and follows the same rules
// as InSelect. A NULL among the statement's results never makes the test true,
// so a NOT IN over a statement that may return NULL matches no rows at all.
func NotInSelect(expression any, statement Select) Membership {
	return Membership{expr: operand(expression), subquery: Scalar(statement), hasSubquery: true, not: true}
}

// Expression returns the expression being tested.
func (m Membership) Expression() Expression {
	return m.expr
}

// Values returns a copy of the tested values. It returns an empty slice for a
// membership test built by InSelect or NotInSelect, whose candidates are the
// statement Subquery returns instead.
func (m Membership) Values() []Expression {
	return append([]Expression(nil), m.values...)
}

// Not reports whether the test is NOT IN.
func (m Membership) Not() bool {
	return m.not
}

// Subquery returns the statement a membership test reads and reports whether the
// test takes one. It reports false for a test built by In or NotIn, whose
// candidates are the value list Values returns.
func (m Membership) Subquery() (Subquery, bool) {
	return m.subquery, m.hasSubquery
}

// Subquery is a SELECT statement used as an expression. It renders as a
// parenthesized SELECT, and the arguments it binds join the enclosing
// statement's argument list at the position the subquery occupies, so
// placeholder numbering stays correct in every dialect.
//
// The statement is validated as part of the statement that encloses it, and it
// reads no table of that statement: every column it names must belong to its own
// FROM or joins. A subquery that reads an enclosing table is refused, because the
// scope rule that makes such a correlation safe is not modelled yet.
type Subquery struct {
	statement Select
}

func (Subquery) expression() {}

// Scalar uses statement as a single value. The statement must project exactly
// one expression, which statement validation checks; that it returns at most one
// row is the caller's responsibility, and the database reports a breach of it.
func Scalar(statement Select) Subquery {
	return Subquery{statement: statement}
}

// Statement returns a copy of the SELECT the subquery runs.
func (s Subquery) Statement() Select {
	return s.statement
}

// FunctionName identifies a SQL function a statement may call.
type FunctionName string

const (
	FunctionCount FunctionName = "COUNT"
	FunctionSum   FunctionName = "SUM"
	FunctionMin   FunctionName = "MIN"
	FunctionMax   FunctionName = "MAX"
	FunctionAvg   FunctionName = "AVG"

	FunctionCoalesce FunctionName = "COALESCE"
	FunctionLower    FunctionName = "LOWER"
	FunctionUpper    FunctionName = "UPPER"
	FunctionAbs      FunctionName = "ABS"

	// FunctionBM25 is SQLite FTS5's ranking function. Its first argument
	// must be a TableIdentifier naming the FTS5 table itself, never a
	// column and never a plain string, which validateBM25Function checks;
	// BM25 builds a call in the right shape directly. Every other argument
	// is an optional per-column weight. Like MATCH, it means nothing outside
	// SQLite, but validation does not gate it by dialect the way it gates
	// OperatorMatch: nothing about the call's shape is dialect-specific the
	// way MATCH's syntax is, and a curated name checked at build time is
	// still better than the Func escape hatch for a call this common.
	// Rendering it against PostgreSQL or MySQL fails only when the database
	// runs the statement, exactly as any other name given to Func would.
	FunctionBM25 FunctionName = "BM25"
)

// Function calls a SQL function on its arguments. COUNT, SUM, MIN, MAX, and
// AVG aggregate, so statement validation accepts a call to one of them only
// where SQL does: in a SELECT projection, in a HAVING clause, or in an ORDER
// BY clause of a statement that groups, never inside another aggregate. An
// ungrouped projection set that both aggregates and reads a column outside an
// aggregate needs an explicit GROUP BY to mean anything, so it is refused, and
// the same rule governs the ORDER BY and HAVING of an ungrouped aggregating
// statement, whose expressions may then read a column only inside an
// aggregate. A grouped statement drops both restrictions: its projections, its
// ORDER BY, and its HAVING clause may read a column freely alongside an
// aggregate. COALESCE, LOWER, UPPER, and ABS are scalar: a call to one of them
// is legal wherever any expression is, including a WHERE clause, a SET
// assignment, an INSERT value, and RETURNING, and its arguments are judged by
// the same placement rules as if they appeared in the call's place directly,
// so an aggregate nested inside one follows the aggregate rule above. Any
// placement Aggregates forbids fails with a ValidationError before the
// statement is rendered.
type Function struct {
	name      FunctionName
	arguments []Expression
	star      bool
	distinct  bool
	unchecked bool
}

func (Function) expression() {}

// Call applies name to arguments. An argument that is not itself an
// Expression is bound the way Bind would bind it. Validation rejects a name
// that is not one of the FunctionName constants, so a function name never
// reaches SQL unchecked.
func Call(name FunctionName, arguments ...any) Function {
	return Function{name: name, arguments: operands(arguments)}
}

// Func calls the SQL function named name on arguments. It is the escape hatch
// for a function outside the curated FunctionName set: name is any string
// rather than a FunctionName constant, and reaching a function this package
// does not curate never requires a change to rasql.
//
// rasql checks nothing about name beyond it being a legal SQL identifier —
// the same rule schema.ValidateIdentifier applies to a column or table name —
// so that a caller-supplied string never reaches SQL text unchecked.
// Validation does not know whether name exists on the target database, what
// it does, what arguments it takes, or whether it renders identically across
// PostgreSQL, MySQL, and SQLite. Portability is entirely the caller's
// responsibility; prefer Call with a FunctionName constant, or one of
// Coalesce, Lower, Upper, and Abs, whenever the function is one of them.
// Every argument still goes through the ordinary expression nodes, so a
// bound value still travels as a placeholder rather than as SQL text — only
// the function's own name reaches SQL unescaped, and only after validation
// has confirmed it is a legal identifier. A call built with Func is always
// treated as scalar: it is legal wherever any expression is, and it is never
// subject to the aggregate placement rules, even when name happens to match
// an aggregate this package curates, such as "SUM". WithDistinct is the one
// thing it does not share with a curated scalar call, and WithDistinct
// states what it does there.
func Func(name string, arguments ...any) Function {
	return Function{name: FunctionName(name), arguments: operands(arguments), unchecked: true}
}

// Count counts the non-NULL values of expression. Use CountAll to count rows.
func Count(expression Expression) Function {
	return Call(FunctionCount, expression)
}

// CountAll counts result rows as COUNT(*).
func CountAll() Function {
	return Function{name: FunctionCount, star: true}
}

// Sum adds the values of expression.
func Sum(expression Expression) Function {
	return Call(FunctionSum, expression)
}

// Min returns the smallest value of expression.
func Min(expression Expression) Function {
	return Call(FunctionMin, expression)
}

// Max returns the largest value of expression.
func Max(expression Expression) Function {
	return Call(FunctionMax, expression)
}

// Avg averages the values of expression.
func Avg(expression Expression) Function {
	return Call(FunctionAvg, expression)
}

// BM25 scores how well table's row matches the query an enclosing MATCH
// tests, lower meaning a better match. weights states a relevance weight per
// column of table, in the column order table declares them, and may be
// omitted entirely; SQLite then weighs every column 1.0. table's own bare
// identifier is always the call's first argument, built the same way Match
// builds its left operand, so the table name never travels as SQL text
// typed by hand.
func BM25(table TableRef, weights ...float64) Function {
	arguments := make([]any, 0, len(weights)+1)
	arguments = append(arguments, TableIdentifier{table: table})
	for _, weight := range weights {
		arguments = append(arguments, weight)
	}
	return Call(FunctionBM25, arguments...)
}

// Coalesce returns the first of expressions that is not NULL. An expression
// that is not itself an Expression is bound the way Bind would bind it. It
// takes at least two expressions, because a one-argument COALESCE is not
// accepted by every supported dialect.
//
// On MySQL, coalescing a decimal column against a bound value changes the
// scale the result decodes at. MySQL fixes the result type while
// it prepares the statement, and a placeholder carries no scale of its own at
// that point, so the whole call becomes the widest decimal the server has,
// DECIMAL(65,30), instead of the column's declared type: a DECIMAL(19,4)
// column decodes with 30 digits right of the point rather than 4. The number
// is the same and only its trailing zeroes differ, but a caller comparing the
// decoded string against a literal has to expect the widened form. Coalescing
// against another decimal expression of the same scale, such as a second
// column, keeps that scale, and so does a driver that interpolates its
// arguments into the SQL text client-side rather than sending them as
// placeholders. PostgreSQL and SQLite are unaffected: both return the value at
// its own scale. The same widening reaches a call built with Func that mixes a
// decimal column with a bound value only when the named function's own MySQL
// type rules resolve a common decimal result type across all of its arguments,
// as IFNULL, GREATEST, and LEAST do. A function that types its result from a
// single argument keeps that argument's scale instead: MySQL types NULLIF from
// its first argument, so NULLIF on a decimal column passed first keeps the
// column's declared scale, while the same call with the placeholder first
// widens. The rule is about which argument the result takes its type from, not
// about the function name. A function that returns a string or an integer,
// such as CONCAT, has no decimal result scale to widen and is unaffected.
func Coalesce(expressions ...any) Function {
	return Call(FunctionCoalesce, expressions...)
}

// Lower returns expression with its letters folded to lower case. SQLite folds
// ASCII letters only, while PostgreSQL and MySQL fold according to the
// server's collation, so a case-insensitive match on non-ASCII text is not
// portable across dialects. MySQL leaves a binary-typed argument unchanged.
func Lower(expression Expression) Function {
	return Call(FunctionLower, expression)
}

// Upper returns expression with its letters folded to upper case. The same
// dialect differences documented on Lower apply here.
func Upper(expression Expression) Function {
	return Call(FunctionUpper, expression)
}

// Abs returns the magnitude of expression.
func Abs(expression Expression) Function {
	return Call(FunctionAbs, expression)
}

// Name returns the called function.
func (f Function) Name() FunctionName {
	return f.name
}

// Arguments returns a copy of the call arguments.
func (f Function) Arguments() []Expression {
	return append([]Expression(nil), f.arguments...)
}

// Star reports whether the call takes * instead of arguments.
func (f Function) Star() bool {
	return f.star
}

// WithDistinct returns a copy of f that evaluates its argument only once per
// distinct value before the function combines them, rendering as e.g.
// COUNT(DISTINCT x). It is a modifier on the call's argument, not a separate
// function name; it takes no argument for the same reason
// query.Select.WithDistinct does. DISTINCT inside a call is only legal SQL
// where the function aggregates, so statement validation rejects it on a
// curated call Aggregates reports false for, and rejects it combined with
// CountAll's star, since COUNT(DISTINCT *) is not legal SQL; call Count with
// a column instead. A call built with Func carries it through
// unchecked, because rasql does not know whether the named function
// aggregates: DISTINCT is how an aggregate this package does not curate, such
// as GROUP_CONCAT, reaches SQL, and whether that call is legal on the target
// database is the caller's responsibility, as everything else about a Func
// name already is.
func (f Function) WithDistinct() Function {
	f.distinct = true
	return f
}

// Distinct reports whether f evaluates its argument only once per distinct
// value before combining them.
func (f Function) Distinct() bool {
	return f.distinct
}

// Aggregates reports whether the called function aggregates. It governs where
// the call is legal: an aggregate is legal only in a SELECT projection, in a
// HAVING clause, and in the ORDER BY of a statement that groups, while a
// scalar call is legal wherever any expression is. A call built with Func is
// always scalar, so it reports false whatever its name, even when that name
// matches an aggregate this package curates, such as "SUM"; that matches the
// placement rule validation applies to it. An unsupported function name also
// reports false; statement validation is what refuses it.
func (f Function) Aggregates() bool {
	if f.unchecked {
		return false
	}
	spec, ok := functionSpecs[f.name]
	return ok && spec.aggregate
}

// ProjectedExpression returns f, so a function call is selected without a
// wrapper. A column and a function call are what a SELECT list mostly holds,
// and both project themselves; Project wraps everything else.
func (f Function) ProjectedExpression() Expression {
	return f
}

// ResultAlias returns an empty string, because an unaliased call is reported
// under whatever name the database picks for it. That name is not portable, so
// a caller reading the result by name should give the call an alias with As.
func (Function) ResultAlias() string {
	return ""
}

// As returns the call projected under alias. It reports no error, because the
// alias is checked when the statement carrying the projection is validated,
// exactly as the alias given to ColumnRef.As is.
func (f Function) As(alias string) Projection {
	return ExpressionProjection{expression: f, alias: alias}
}
