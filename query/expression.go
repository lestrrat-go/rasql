package query

// Expression is a dialect-neutral SQL expression.
type Expression interface {
	expression()
}

// Column is a typed reference to a table column.
type Column struct {
	source Table
	name   string
}

func (Column) expression() {}

// Name returns the column name.
func (c Column) Name() string {
	return c.name
}

// Source returns the table that owns the column.
func (c Column) Source() Table {
	return c.source
}

// ExcludedColumn references the incoming value of a column during an upsert.
type ExcludedColumn struct {
	column Column
}

func (ExcludedColumn) expression() {}

// Excluded references the incoming value for column in an upsert assignment.
func Excluded(column Column) ExcludedColumn {
	return ExcludedColumn{column: column}
}

// Column returns the incoming column.
func (c ExcludedColumn) Column() Column {
	return c.column
}

// Value is a bound SQL argument. Its value is never interpolated into SQL text.
type Value struct {
	value any
}

func (Value) expression() {}

// Bind creates a bound SQL argument expression.
func Bind(value any) Value {
	return Value{value: value}
}

// Argument returns the value supplied to Bind.
func (v Value) Argument() any {
	return v.value
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
)

// Binary combines two expressions with an operator.
type Binary struct {
	left     Expression
	operator BinaryOperator
	right    Expression
}

func (Binary) expression() {}

// Compare combines left and right with operator.
func Compare(left Expression, operator BinaryOperator, right Expression) Binary {
	return Binary{left: left, operator: operator, right: right}
}

// Equal compares left and right for equality.
func Equal(left Expression, right Expression) Binary {
	return Compare(left, OperatorEqual, right)
}

// NotEqual compares left and right for inequality.
func NotEqual(left Expression, right Expression) Binary {
	return Compare(left, OperatorNotEqual, right)
}

// GreaterThan compares left and right.
func GreaterThan(left Expression, right Expression) Binary {
	return Compare(left, OperatorGreaterThan, right)
}

// GreaterThanOrEqual compares left and right.
func GreaterThanOrEqual(left Expression, right Expression) Binary {
	return Compare(left, OperatorGreaterThanOrEqual, right)
}

// LessThan compares left and right.
func LessThan(left Expression, right Expression) Binary {
	return Compare(left, OperatorLessThan, right)
}

// LessThanOrEqual compares left and right.
func LessThanOrEqual(left Expression, right Expression) Binary {
	return Compare(left, OperatorLessThanOrEqual, right)
}

// Like compares left and right with SQL LIKE.
func Like(left Expression, right Expression) Binary {
	return Compare(left, OperatorLike, right)
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
// It renders as expression IN (…). The value list takes expressions, so a column
// or a computed operand is accepted as deliberately as a bound value. Each Bind
// value renders as its own placeholder and costs one argument, while a column
// renders as a quoted identifier and costs none. Statement validation rejects an
// empty value list, because IN () is not valid SQL in any supported dialect, and
// rejects a Subquery placed among the values; use InSelect for
// expression IN (SELECT …).
func In(expression Expression, values ...Expression) Membership {
	return Membership{expr: expression, values: append([]Expression(nil), values...)}
}

// NotIn tests whether expression differs from every one of values.
// It renders as expression NOT IN (…) and follows the same rules as In,
// including the rejection of an empty value list and of a Subquery among the
// values by statement validation; use NotInSelect for
// expression NOT IN (SELECT …). A NULL among values never makes the test true:
// it is false when a non-NULL value equals expression, and unknown otherwise. A
// WHERE clause keeps neither, so a NOT IN over a list that may contain NULL
// matches no rows at all.
func NotIn(expression Expression, values ...Expression) Membership {
	return Membership{expr: expression, values: append([]Expression(nil), values...), not: true}
}

// InSelect tests whether expression equals a value the statement returns.
// It renders as expression IN (SELECT …). The statement must project exactly one
// expression, which statement validation checks. Unlike In, it costs no argument
// per candidate value, so a set of any size fits within the dialect's parameter
// limit.
func InSelect(expression Expression, statement Select) Membership {
	return Membership{expr: expression, subquery: Scalar(statement), hasSubquery: true}
}

// NotInSelect tests whether expression differs from every value the statement
// returns. It renders as expression NOT IN (SELECT …) and follows the same rules
// as InSelect. A NULL among the statement's results never makes the test true,
// so a NOT IN over a statement that may return NULL matches no rows at all.
func NotInSelect(expression Expression, statement Select) Membership {
	return Membership{expr: expression, subquery: Scalar(statement), hasSubquery: true, not: true}
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
)

// Function calls a SQL function on its arguments. Every supported function
// aggregates, so statement validation accepts a call only where SQL does: in a
// SELECT projection, in a HAVING clause, or in an ORDER BY clause of a
// statement that groups, never inside another aggregate. An ungrouped
// projection set that both aggregates and reads a column outside an aggregate
// needs an explicit GROUP BY to mean anything, so it is refused, and the same
// rule governs the ORDER BY and HAVING of an ungrouped aggregating statement,
// whose expressions may then read a column only inside an aggregate. A grouped
// statement drops both restrictions: its projections, its ORDER BY, and its
// HAVING clause may read a column freely alongside an aggregate. Any other
// placement fails with a ValidationError before the statement is rendered.
type Function struct {
	name      FunctionName
	arguments []Expression
	star      bool
}

func (Function) expression() {}

// Call applies name to arguments. Validation rejects a name that is not one of
// the FunctionName constants, so a function name never reaches SQL unchecked.
func Call(name FunctionName, arguments ...Expression) Function {
	return Function{name: name, arguments: append([]Expression(nil), arguments...)}
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
