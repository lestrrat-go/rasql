package query_test

import (
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestSelectBuildsImmutableStatement(t *testing.T) {
	users, err := query.NewTable(usersTable())
	require.NoError(t, err)
	users, err = users.As("u")
	require.NoError(t, err)
	orders, err := query.NewTable(ordersTable())
	require.NoError(t, err)
	orders, err = orders.As("o")
	require.NoError(t, err)

	userID, err := users.Column("id")
	require.NoError(t, err)
	orderUserID, err := orders.Column("user_id")
	require.NoError(t, err)
	amount, err := orders.Column("amount")
	require.NoError(t, err)

	statement, err := query.NewSelect(users, query.Project(userID).As("user_id"))
	require.NoError(t, err)
	statement, err = statement.WithJoin(query.InnerJoin(orders, query.Equal(userID, orderUserID)))
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.And(
		query.GreaterThan(amount, query.Bind(100)),
		query.IsNotNull(orderUserID),
	))
	require.NoError(t, err)
	statement, err = statement.WithOrder(query.Desc(amount))
	require.NoError(t, err)
	statement, err = statement.WithLimit(20)
	require.NoError(t, err)
	statement, err = statement.WithOffset(10)
	require.NoError(t, err)

	require.Len(t, statement.Projections(), 1)
	require.Len(t, statement.Joins(), 1)
	require.Len(t, statement.OrderBy(), 1)
	limit, ok := statement.Limit()
	require.True(t, ok)
	require.Equal(t, 20, limit)
	offset, ok := statement.Offset()
	require.True(t, ok)
	require.Equal(t, 10, offset)
	require.NoError(t, statement.Validate())
}

func TestFunctionConstructorsCarryTheirCall(t *testing.T) {
	users, err := query.NewTable(usersTable())
	require.NoError(t, err)
	userID, err := users.Column("id")
	require.NoError(t, err)

	tests := map[string]struct {
		function  query.Function
		name      query.FunctionName
		arguments []query.Expression
		star      bool
	}{
		"Count":    {function: query.Count(userID), name: query.FunctionCount, arguments: []query.Expression{userID}},
		"CountAll": {function: query.CountAll(), name: query.FunctionCount, star: true},
		"Sum":      {function: query.Sum(userID), name: query.FunctionSum, arguments: []query.Expression{userID}},
		"Min":      {function: query.Min(userID), name: query.FunctionMin, arguments: []query.Expression{userID}},
		"Max":      {function: query.Max(userID), name: query.FunctionMax, arguments: []query.Expression{userID}},
		"Avg":      {function: query.Avg(userID), name: query.FunctionAvg, arguments: []query.Expression{userID}},
		"Call":     {function: query.Call(query.FunctionSum, userID), name: query.FunctionSum, arguments: []query.Expression{userID}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, test.name, test.function.Name())
			require.Equal(t, test.arguments, test.function.Arguments())
			require.Equal(t, test.star, test.function.Star())
		})
	}

	function := query.Sum(userID)
	arguments := function.Arguments()
	arguments[0] = nil
	require.Equal(t, userID, function.Arguments()[0], "mutating the returned slice must not change the expression")
}

func TestSelectRejectsInvalidStatements(t *testing.T) {
	users, err := query.NewTable(usersTable())
	require.NoError(t, err)
	userID, err := users.Column("id")
	require.NoError(t, err)

	_, err = query.NewSelect(users)
	requireQueryValidationError(t, err)

	statement, err := query.NewSelect(users, query.Project(userID))
	require.NoError(t, err)

	_, err = statement.WithWhere(query.And(userID))
	requireQueryValidationError(t, err)

	_, err = statement.WithLimit(-1)
	requireQueryValidationError(t, err)

	other, err := query.NewTable(ordersTable())
	require.NoError(t, err)
	otherID, err := other.Column("id")
	require.NoError(t, err)
	_, err = statement.WithWhere(query.Equal(otherID, query.Bind(1)))
	requireQueryValidationError(t, err)

	_, err = statement.WithWhere(query.In(userID))
	requireQueryValidationError(t, err)
	_, err = statement.WithWhere(query.In(userID, query.Bind(1), nil))
	requireQueryValidationError(t, err)
	_, err = statement.WithWhere(query.In(otherID, query.Bind(1)))
	requireQueryValidationError(t, err)

	_, err = query.NewSelect(users, query.Project(query.Call("LOWER", userID)))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, `unsupported function "LOWER"`)

	_, err = query.NewSelect(users, query.Project(query.Call(query.FunctionSum)))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "takes exactly one argument, got 0")

	_, err = query.NewSelect(users, query.Project(query.Call(query.FunctionCount, userID, userID)))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "takes exactly one argument, got 2")

	_, err = query.NewSelect(users, query.Project(query.Count(otherID)))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "references table")
	require.ErrorContains(t, err, "outside the statement")

	statement, err = query.NewSelect(users,
		query.Project(query.CountAll()),
		query.Project(query.Max(userID)),
	)
	require.NoError(t, err)
	require.NoError(t, statement.Validate())
}

func TestSelectAcceptsMembershipPredicate(t *testing.T) {
	users, err := query.NewTable(usersTable())
	require.NoError(t, err)
	userID, err := users.Column("id")
	require.NoError(t, err)
	statement, err := query.NewSelect(users, query.Project(userID))
	require.NoError(t, err)

	in := query.In(userID, query.Bind(1), query.Bind(2), query.Bind(3))
	statement, err = statement.WithWhere(in)
	require.NoError(t, err)
	require.Equal(t, query.Expression(in), statement.Where())

	notIn := query.NotIn(userID, query.Bind(1))
	statement, err = statement.WithWhere(notIn)
	require.NoError(t, err)
	require.Equal(t, query.Expression(notIn), statement.Where())
}

func TestTableRejectsUnknownColumn(t *testing.T) {
	users, err := query.NewTable(usersTable())
	require.NoError(t, err)

	_, err = users.Column("missing")
	require.Error(t, err)
}

func TestTableCopiesDescriptor(t *testing.T) {
	descriptor := usersTable()
	users, err := query.NewTable(descriptor)
	require.NoError(t, err)

	descriptor.Columns[0].Name = "changed"
	descriptor.PrimaryKey[0] = "changed"

	_, err = users.Column("id")
	require.NoError(t, err)
	require.Equal(t, []string{"id"}, users.Definition().PrimaryKey)
}

func TestMustNewTable(t *testing.T) {
	users := query.MustNewTable(usersTable())
	_, err := users.Column("id")
	require.NoError(t, err)

	require.Panics(t, func() {
		query.MustNewTable(schema.Table{})
	})
}

func requireQueryValidationError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var validationErr *query.ValidationError
	require.True(t, errors.As(err, &validationErr))
}

func usersTable() schema.Table {
	return schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	}
}

func ordersTable() schema.Table {
	return schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
			{Name: "amount", Type: schema.TypeFloat},
		},
		PrimaryKey: []string{"id"},
	}
}
