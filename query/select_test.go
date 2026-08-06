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

	_, err = statement.WithWhere(query.GreaterThan(userID, query.Scalar(query.Select{})))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "from")

	twoProjections, err := query.NewSelect(users, query.Project(userID), query.Project(query.Bind(1)))
	require.NoError(t, err)
	_, err = statement.WithWhere(query.GreaterThan(userID, query.Scalar(twoProjections)))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "must select exactly one expression, got 2")

	_, err = statement.WithWhere(query.In(userID, query.Scalar(twoProjections)))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "use InSelect or NotInSelect")

	// A subquery cannot correlate: its WHERE reads no table outside its own FROM
	// and joins, so a caller who tries to reference the enclosing table hits the
	// same "outside the statement" error the table-scope check already reports
	// for any expression, before the subquery is even nested inside another
	// statement.
	correlated, err := query.NewSelect(other, query.Project(otherID))
	require.NoError(t, err)
	_, err = correlated.WithWhere(query.Equal(userID, query.Bind(1)))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "references table")
	require.ErrorContains(t, err, "outside the statement")

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

// TestSelectAcceptsSubqueryPredicates covers the placements a subquery is legal
// in: as the right-hand side of an InSelect/NotInSelect membership test, and as
// a Scalar operand of a comparison, including one nested two levels deep.
func TestSelectAcceptsSubqueryPredicates(t *testing.T) {
	users, err := query.NewTable(usersTable())
	require.NoError(t, err)
	userID, err := users.Column("id")
	require.NoError(t, err)
	orders, err := query.NewTable(ordersTable())
	require.NoError(t, err)
	orderID, err := orders.Column("id")
	require.NoError(t, err)
	orderUserID, err := orders.Column("user_id")
	require.NoError(t, err)
	amount, err := orders.Column("amount")
	require.NoError(t, err)

	statement, err := query.NewSelect(users, query.Project(userID))
	require.NoError(t, err)

	orderedUserIDs, err := query.NewSelect(orders, query.Project(orderUserID))
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.InSelect(userID, orderedUserIDs))
	require.NoError(t, err)
	require.NoError(t, statement.Validate())

	averageAmount, err := query.NewSelect(orders, query.Project(query.Avg(amount)))
	require.NoError(t, err)
	require.NoError(t, statement.Validate())
	statement, err = statement.WithWhere(query.GreaterThanOrEqual(userID, query.Scalar(averageAmount)))
	require.NoError(t, err)
	require.NoError(t, statement.Validate())

	notInStatement, err := query.NewSelect(users, query.Project(userID))
	require.NoError(t, err)
	notInStatement, err = notInStatement.WithWhere(query.NotInSelect(userID, orderedUserIDs))
	require.NoError(t, err)
	require.NoError(t, notInStatement.Validate())

	// A subquery two levels deep: the outer statement's InSelect reads a
	// statement whose own WHERE runs another InSelect.
	innermost, err := query.NewSelect(orders, query.Project(orderID))
	require.NoError(t, err)
	innermost, err = innermost.WithWhere(query.GreaterThan(amount, query.Bind(10)))
	require.NoError(t, err)
	nested, err := query.NewSelect(orders, query.Project(orderUserID))
	require.NoError(t, err)
	nested, err = nested.WithWhere(query.InSelect(orderID, innermost))
	require.NoError(t, err)
	deep, err := query.NewSelect(users, query.Project(userID))
	require.NoError(t, err)
	deep, err = deep.WithWhere(query.InSelect(userID, nested))
	require.NoError(t, err)
	require.NoError(t, deep.Validate())
}

// TestSelectRejectsMisplacedAggregates covers the placement rules that make an
// aggregate legal SQL. Every rejected shape below rendered SQL that a supported
// database refuses or answers meaninglessly; TestSQLiteRefusesMisplacedAggregates
// in the root package runs the SQLite half against a real database.
func TestSelectRejectsMisplacedAggregates(t *testing.T) {
	users, err := query.NewTable(usersTable())
	require.NoError(t, err)
	userID, err := users.Column("id")
	require.NoError(t, err)
	orders, err := query.NewTable(ordersTable())
	require.NoError(t, err)
	orderUserID, err := orders.Column("user_id")
	require.NoError(t, err)

	base, err := query.NewSelect(users, query.Project(userID))
	require.NoError(t, err)

	tests := map[string]struct {
		build   func() error
		message string
	}{
		"where": {
			build: func() error {
				_, err := base.WithWhere(query.GreaterThan(query.Count(userID), query.Bind(1)))
				return err
			},
			message: `calls aggregate function "COUNT" in a WHERE clause`,
		},
		"join on": {
			build: func() error {
				_, err := base.WithJoin(query.InnerJoin(orders, query.Equal(query.Count(userID), orderUserID)))
				return err
			},
			message: `calls aggregate function "COUNT" in a JOIN ON condition`,
		},
		"order by a projection set that does not aggregate": {
			build: func() error {
				_, err := base.WithOrder(query.Asc(query.Count(userID)))
				return err
			},
			message: `calls aggregate function "COUNT" in an ORDER BY clause`,
		},
		"column ordering an aggregate projection set": {
			build: func() error {
				aggregated, err := query.NewSelect(users, query.Project(query.CountAll()))
				if err != nil {
					return err
				}
				_, err = aggregated.WithOrder(query.Asc(userID))
				return err
			},
			message: "reads a column outside an aggregate function while the projections aggregate",
		},
		"column beside an aggregate in one ordering expression": {
			build: func() error {
				aggregated, err := query.NewSelect(users, query.Project(query.CountAll()))
				if err != nil {
					return err
				}
				_, err = aggregated.WithOrder(query.Asc(query.GreaterThan(query.Count(userID), userID)))
				return err
			},
			message: "reads a column outside an aggregate function while the projections aggregate",
		},
		"nested aggregate in an ordering expression": {
			build: func() error {
				aggregated, err := query.NewSelect(users, query.Project(query.CountAll()))
				if err != nil {
					return err
				}
				_, err = aggregated.WithOrder(query.Asc(query.Max(query.Max(userID))))
				return err
			},
			message: `calls aggregate function "MAX" inside another aggregate function`,
		},
		"nested aggregate": {
			build: func() error {
				_, err := query.NewSelect(users, query.Project(query.Sum(query.Sum(userID))))
				return err
			},
			message: `calls aggregate function "SUM" inside another aggregate function`,
		},
		"aggregate nested below an operator": {
			build: func() error {
				_, err := query.NewSelect(users, query.Project(query.Max(query.GreaterThan(query.Count(userID), query.Bind(1)))))
				return err
			},
			message: `calls aggregate function "COUNT" inside another aggregate function`,
		},
		"column projected beside an aggregate": {
			build: func() error {
				_, err := query.NewSelect(users, query.Project(userID), query.Project(query.CountAll()))
				return err
			},
			message: "reads a column outside an aggregate function while projections[1] aggregates",
		},
		"column beside an aggregate in one projection": {
			build: func() error {
				_, err := query.NewSelect(users, query.Project(query.GreaterThan(query.Count(userID), userID)))
				return err
			},
			message: "reads a column outside an aggregate function while projections[0] aggregates",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := test.build()
			requireQueryValidationError(t, err)
			require.ErrorContains(t, err, test.message)
		})
	}
}

// TestSelectAcceptsWellPlacedAggregates pins the statements the placement rules
// must keep accepting, so the rules reject a shape rather than the feature.
func TestSelectAcceptsWellPlacedAggregates(t *testing.T) {
	users, err := query.NewTable(usersTable())
	require.NoError(t, err)
	userID, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)
	orders, err := query.NewTable(ordersTable())
	require.NoError(t, err)
	orderUserID, err := orders.Column("user_id")
	require.NoError(t, err)

	// An aggregate-only projection set filters and joins by columns, because
	// WHERE and JOIN ON run before aggregation on the source rows.
	statement, err := query.NewSelect(users,
		query.Project(query.CountAll()).As("total"),
		query.Project(query.Max(userID)).As("top"),
	)
	require.NoError(t, err)
	statement, err = statement.WithJoin(query.InnerJoin(orders, query.Equal(userID, orderUserID)))
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.IsNotNull(email))
	require.NoError(t, err)
	require.NoError(t, statement.Validate())

	// ORDER BY runs after aggregation, so an aggregate-only statement orders by
	// anything that reads no column outside an aggregate.
	for name, order := range map[string]query.Order{
		"aggregate":                   query.Asc(query.CountAll()),
		"aggregate over a column":     query.Desc(query.Max(userID)),
		"expression over aggregates":  query.Asc(query.GreaterThan(query.Count(userID), query.Bind(1))),
		"null test over an aggregate": query.Asc(query.IsNull(query.Max(userID))),
		"bound value":                 query.Asc(query.Bind(1)),
	} {
		t.Run(name, func(t *testing.T) {
			ordered, err := statement.WithOrder(order)
			require.NoError(t, err)
			require.NoError(t, ordered.Validate())
		})
	}

	// A projection set that never aggregates keeps reading columns freely, in
	// the projections and in the ordering alike.
	columns, err := query.NewSelect(users, query.Project(userID), query.Project(email))
	require.NoError(t, err)
	columns, err = columns.WithOrder(query.Asc(email))
	require.NoError(t, err)
	require.NoError(t, columns.Validate())
}

// TestSelectJudgesAggregatesInsideMembership pins how the clause-aware walk
// treats a membership test. The test itself is an ordinary predicate, so the
// tested expression and every member of its value list are judged by the rule
// that governs the clause the test sits in, exactly as the same operand would be
// outside an IN.
func TestSelectJudgesAggregatesInsideMembership(t *testing.T) {
	users, err := query.NewTable(usersTable())
	require.NoError(t, err)
	userID, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)

	base, err := query.NewSelect(users, query.Project(userID))
	require.NoError(t, err)
	aggregated, err := query.NewSelect(users, query.Project(query.CountAll()))
	require.NoError(t, err)

	rejected := map[string]struct {
		build   func() error
		message string
	}{
		"aggregate in a value list of a WHERE membership": {
			build: func() error {
				_, err := base.WithWhere(query.In(userID, query.Bind(1), query.Count(email)))
				return err
			},
			message: `calls aggregate function "COUNT" in a WHERE clause`,
		},
		"aggregate tested by a WHERE membership": {
			build: func() error {
				_, err := base.WithWhere(query.NotIn(query.CountAll(), query.Bind(1)))
				return err
			},
			message: `calls aggregate function "COUNT" in a WHERE clause`,
		},
		"aggregate in a value list inside another aggregate": {
			build: func() error {
				_, err := query.NewSelect(users, query.Project(query.Max(query.In(userID, query.Count(email)))))
				return err
			},
			message: `calls aggregate function "COUNT" inside another aggregate function`,
		},
		"column in a value list beside an aggregate projection": {
			build: func() error {
				_, err := query.NewSelect(users,
					query.Project(query.In(email, query.Bind("ada@example.com"))),
					query.Project(query.CountAll()),
				)
				return err
			},
			message: "reads a column outside an aggregate function while projections[1] aggregates",
		},
		"column in a value list ordering an aggregate projection set": {
			build: func() error {
				_, err := aggregated.WithOrder(query.Asc(query.In(query.CountAll(), userID)))
				return err
			},
			message: "reads a column outside an aggregate function while the projections aggregate",
		},
		"empty value list in an ordering expression": {
			build: func() error {
				_, err := aggregated.WithOrder(query.Asc(query.In(query.CountAll())))
				return err
			},
			message: "requires at least one value",
		},
	}
	for name, test := range rejected {
		t.Run(name, func(t *testing.T) {
			err := test.build()
			requireQueryValidationError(t, err)
			require.ErrorContains(t, err, test.message)
		})
	}

	accepted := map[string]func() error{
		// WHERE runs before aggregation, so a membership test over columns and
		// bound values stays legal in a statement that never aggregates.
		"columns and values in a WHERE membership": func() error {
			_, err := base.WithWhere(query.In(userID, query.Bind(1), query.Bind(2)))
			return err
		},
		// A column read inside an aggregate is aggregated, including one the
		// aggregate reaches through a membership test.
		"membership inside an aggregate projection": func() error {
			_, err := query.NewSelect(users, query.Project(query.Count(query.In(userID, query.Bind(1), query.Bind(2)))))
			return err
		},
		// ORDER BY of an aggregate-only statement may call an aggregate, so a
		// membership test whose operands are all aggregates or values belongs there.
		"membership over aggregates in an ORDER BY": func() error {
			_, err := aggregated.WithOrder(query.Asc(query.In(query.CountAll(), query.Bind(1), query.Max(userID))))
			return err
		},
	}
	for name, build := range accepted {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, build())
		})
	}
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
