package query_test

import (
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/query"
	"github.com/stretchr/testify/require"
)

// TestColumnRefValidate pins ColumnRef.Validate for a valid column and for
// the zero ColumnRef, whose nil source is reported rather than collapsed into
// the missing-column message.
func TestColumnRefValidate(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	id := users.Column("id")
	require.NoError(t, id.Validate())

	var zero query.ColumnRef
	require.True(t, errors.Is(zero.Validate(), query.ErrNilTable))
}

// TestColumnRefValidateReportsMissingColumn covers ColumnRef.Validate for a
// name its source table does not hold, on an unqualified, a qualified, and an
// aliased table.
func TestColumnRefValidateReportsMissingColumn(t *testing.T) {
	definition := usersTable()

	users, err := query.NewTableRef(definition)
	require.NoError(t, err)
	require.ErrorContains(t, users.Column("missing").Validate(), `table "users" has no column "missing"`)

	definition.Schema = "tenant"
	qualified := query.MustTableRef(definition)
	require.ErrorContains(t, qualified.Column("missing").Validate(), `table "tenant.users" has no column "missing"`)

	aliased, err := qualified.As("u")
	require.NoError(t, err)
	require.ErrorContains(t, aliased.Column("missing").Validate(), `table "u" has no column "missing"`)
}

func TestMembershipKeepsOperands(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	id := users.Column("id")

	values := []any{query.Bind(1), query.Bind(2)}
	in := query.In(id, values...)
	require.Equal(t, query.Expression(id), in.Expression())
	require.Equal(t, []query.Expression{query.Bind(1), query.Bind(2)}, in.Values())
	require.False(t, in.Not())

	notIn := query.NotIn(id, values...)
	require.Equal(t, query.Expression(id), notIn.Expression())
	require.Equal(t, []query.Expression{query.Bind(1), query.Bind(2)}, notIn.Values())
	require.True(t, notIn.Not())

	// Mutating the slice returned by Values does not change the expression.
	returned := in.Values()
	returned[0] = query.Bind(99)
	require.Equal(t, []query.Expression{query.Bind(1), query.Bind(2)}, in.Values())

	// Mutating the variadic slice passed in does not change the expression either.
	values[0] = query.Bind(99)
	require.Equal(t, []query.Expression{query.Bind(1), query.Bind(2)}, in.Values())
}

func TestSubqueryKeepsItsStatement(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")
	statement, err := query.NewSelect(users, userID)
	require.NoError(t, err)

	subquery := query.Scalar(statement)
	require.Equal(t, statement, subquery.Statement())

	// Appending to the returned statement's projections does not change the
	// subquery, because Projections already returns a defensive copy and
	// Select's With… methods never mutate the receiver.
	returned := subquery.Statement()
	_, err = returned.WithOrder(query.Asc(userID))
	require.NoError(t, err)
	require.Equal(t, statement, subquery.Statement())
}

func TestMembershipReportsItsSubqueryForm(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")
	statement, err := query.NewSelect(users, userID)
	require.NoError(t, err)

	in := query.InSelect(userID, statement)
	subquery, ok := in.Subquery()
	require.True(t, ok)
	require.Equal(t, statement, subquery.Statement())
	require.Empty(t, in.Values())
	require.False(t, in.Not())

	notIn := query.NotInSelect(userID, statement)
	subquery, ok = notIn.Subquery()
	require.True(t, ok)
	require.Equal(t, statement, subquery.Statement())
	require.Empty(t, notIn.Values())
	require.True(t, notIn.Not())

	plainIn := query.In(userID, query.Bind(1))
	_, ok = plainIn.Subquery()
	require.False(t, ok)

	plainNotIn := query.NotIn(userID, query.Bind(1))
	_, ok = plainNotIn.Subquery()
	require.False(t, ok)
}

// TestComparisonBindsPlainValues pins that every comparison constructor binds
// a plain Go value on its right side exactly the way an explicit Bind would.
func TestComparisonBindsPlainValues(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	email := users.Column("email")

	tests := []struct {
		name  string
		right query.Expression
	}{
		{"Equal", query.Equal(email, "ada@example.com").Right()},
		{"NotEqual", query.NotEqual(email, "ada@example.com").Right()},
		{"GreaterThan", query.GreaterThan(email, "ada@example.com").Right()},
		{"GreaterThanOrEqual", query.GreaterThanOrEqual(email, "ada@example.com").Right()},
		{"LessThan", query.LessThan(email, "ada@example.com").Right()},
		{"LessThanOrEqual", query.LessThanOrEqual(email, "ada@example.com").Right()},
		{"Like", query.Like(email, "ada@example.com").Right()},
		{"Compare", query.Compare(email, query.OperatorEqual, "ada@example.com").Right()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, query.Bind("ada@example.com"), test.right)
		})
	}
}

// TestComparisonKeepsExpressions pins that an operand already satisfying
// Expression is used as it stands, including the no-double-wrap guarantee
// for a value already built with Bind.
func TestComparisonKeepsExpressions(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")
	orders, err := query.NewTableRef(ordersTable())
	require.NoError(t, err)
	orderUserID := orders.Column("user_id")

	require.Equal(t, query.Expression(orderUserID), query.Equal(userID, orderUserID).Right())

	right := query.Equal(userID, query.Bind(1)).Right()
	require.Equal(t, query.Bind(1), right)
	value, ok := right.(query.Value)
	require.True(t, ok)
	require.Equal(t, 1, value.Argument())
}

// TestComparisonBindsPlainValueOnTheLeft pins that binding applies to either
// operand, not only the right one.
func TestComparisonBindsPlainValueOnTheLeft(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")

	require.Equal(t, query.Bind(1), query.Equal(1, userID).Left())
}

// TestComparisonBindsNil pins that nil binds as a bound NULL rather than
// failing to compile or failing validation at the comparison itself.
func TestComparisonBindsNil(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	email := users.Column("email")

	right := query.Equal(email, nil).Right()
	require.Equal(t, query.Bind(nil), right)
	value, ok := right.(query.Value)
	require.True(t, ok)
	require.Nil(t, value.Argument())
}

// TestComparisonBindsTypedNilPointer pins that a typed nil pointer binds as
// itself, not as an untyped nil — a nullable-column write needs the typed
// nil to reach database/sql, which converts it to NULL.
func TestComparisonBindsTypedNilPointer(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	email := users.Column("email")

	var p *string
	right := query.Equal(email, p).Right()
	value, ok := right.(query.Value)
	require.True(t, ok)
	argument, ok := value.Argument().(*string)
	require.True(t, ok)
	require.Nil(t, argument)
}

// TestMembershipBindsPlainValues pins that In binds each plain-value argument
// while keeping an operand that is already an Expression unwrapped.
func TestMembershipBindsPlainValues(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")
	orders, err := query.NewTableRef(ordersTable())
	require.NoError(t, err)
	orderUserID := orders.Column("user_id")

	require.Equal(t, query.In(userID, query.Bind(1), query.Bind(2), query.Bind(3)), query.In(userID, 1, 2, 3))
	require.Equal(t, []query.Expression{orderUserID}, query.In(userID, orderUserID).Values())
}

// TestMembershipBindsSliceAsOneValue pins the documented no-expansion rule: a
// slice argument is bound whole as a single parameter, never expanded.
func TestMembershipBindsSliceAsOneValue(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")

	in := query.In(userID, []int{1, 2})
	values := in.Values()
	require.Len(t, values, 1)
	value, ok := values[0].(query.Value)
	require.True(t, ok)
	require.Equal(t, []int{1, 2}, value.Argument())
}

// TestFunctionCallBindsPlainValues pins that Coalesce, Func, and Call bind a
// plain-value argument while keeping a ColumnRef argument unwrapped.
func TestFunctionCallBindsPlainValues(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	email := users.Column("email")

	coalesce := query.Coalesce(email, "")
	require.Equal(t, []query.Expression{email, query.Bind("")}, coalesce.Arguments())

	fn := query.Func("jsonb_path_query", email, "$.a")
	require.Equal(t, []query.Expression{email, query.Bind("$.a")}, fn.Arguments())

	call := query.Call(query.FunctionCoalesce, email, "")
	require.Equal(t, []query.Expression{email, query.Bind("")}, call.Arguments())
}

// embeddedExpression satisfies query.Expression by embedding a query type. A
// nil *embeddedExpression must still fail validation, which is what pins
// that validateExpression's existing reflect.Pointer guard covers the case
// without any new nil check in operand — internal/nilcheck is deliberately
// not used here because it would also reject a legitimate typed nil pointer
// bound as data (see TestComparisonBindsTypedNilPointer).
type embeddedExpression struct{ query.Value }

// TestNilExpressionSatisfyingTypeIsRefused pins that a nil pointer to a
// caller type embedding a query type is still refused by statement
// validation, even though it satisfies the Expression interface and so
// passes operand's type assertion.
func TestNilExpressionSatisfyingTypeIsRefused(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")
	statement, err := query.NewSelect(users, userID)
	require.NoError(t, err)

	var nilExpression *embeddedExpression
	_, err = statement.WithWhere(query.Equal(userID, nilExpression))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "must not be nil")
}
