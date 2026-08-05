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
	expr   Expression
	values []Expression
	not    bool
}

func (Membership) expression() {}

// In tests whether expression equals one of values.
// It renders as expression IN (…) with one placeholder per bound value, so a
// long list costs one argument per element. Statement validation rejects an
// empty value list, because IN () is not valid SQL in any supported dialect.
func In(expression Expression, values ...Expression) Membership {
	return Membership{expr: expression, values: append([]Expression(nil), values...)}
}

// NotIn tests whether expression differs from every one of values.
// It renders as expression NOT IN (…) and follows the same rules as In. A NULL
// among values makes the whole test unknown, which is SQL's rule for NOT IN.
func NotIn(expression Expression, values ...Expression) Membership {
	return Membership{expr: expression, values: append([]Expression(nil), values...), not: true}
}

// Expression returns the expression being tested.
func (m Membership) Expression() Expression {
	return m.expr
}

// Values returns a copy of the tested values.
func (m Membership) Values() []Expression {
	return append([]Expression(nil), m.values...)
}

// Not reports whether the test is NOT IN.
func (m Membership) Not() bool {
	return m.not
}
