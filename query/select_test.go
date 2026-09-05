package query_test

import (
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestSelectBuildsImmutableStatement(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	users, err = users.As("u")
	require.NoError(t, err)
	orders, err := query.NewTableRef(ordersTable())
	require.NoError(t, err)
	orders, err = orders.As("o")
	require.NoError(t, err)

	userID := users.Column("id")
	orderUserID := orders.Column("user_id")
	amount := orders.Column("amount")

	statement, err := query.NewSelect(users, userID.As("user_id"))
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

// TestSelectAcceptsDistinctStatements proves WithDistinct sets Distinct and
// leaves the receiver untouched, the same immutability
// TestSelectBuildsImmutableStatement pins for the other With… methods.
func TestSelectAcceptsDistinctStatements(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	email := users.Column("email")

	base, err := query.NewSelect(users, email)
	require.NoError(t, err)
	require.False(t, base.Distinct())

	distinct, err := base.WithDistinct()
	require.NoError(t, err)
	require.True(t, distinct.Distinct())
	require.False(t, base.Distinct(), "WithDistinct must not mutate the receiver")
}

// TestSelectAcceptsDistinctWithGroupingAndPaging proves distinct composes with
// every clause the design table in .tmp/design-p2-distinct.md lists as
// unchecked: GROUP BY, HAVING, LIMIT, and OFFSET, in either call order with
// WithDistinct. It also proves that an ORDER BY term outside the projections
// of a distinct statement validates: rasql does not implement the refusal
// PostgreSQL (42P10) and MySQL (3065) apply, and leaves it to the database.
// TestSelectRendersDistinctStatement and distinct_order_integration_test.go
// prove the rendering and the server-side behavior this decision relies on.
func TestSelectAcceptsDistinctWithGroupingAndPaging(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	email := users.Column("email")
	userID := users.Column("id")

	grouped, err := query.NewGroupedSelect(users, []query.Expression{email}, email)
	require.NoError(t, err)
	grouped, err = grouped.WithHaving(query.NotEqual(email, query.Bind("done")))
	require.NoError(t, err)
	grouped, err = grouped.WithDistinct()
	require.NoError(t, err)
	grouped, err = grouped.WithLimit(10)
	require.NoError(t, err)
	grouped, err = grouped.WithOffset(5)
	require.NoError(t, err)
	require.NoError(t, grouped.Validate())
	require.True(t, grouped.Distinct())

	// The reverse call order, WithDistinct before the other clauses, reaches
	// the same accepted statement.
	reversed, err := query.NewGroupedSelect(users, []query.Expression{email}, email)
	require.NoError(t, err)
	reversed, err = reversed.WithDistinct()
	require.NoError(t, err)
	reversed, err = reversed.WithHaving(query.NotEqual(email, query.Bind("done")))
	require.NoError(t, err)
	reversed, err = reversed.WithLimit(10)
	require.NoError(t, err)
	reversed, err = reversed.WithOffset(5)
	require.NoError(t, err)
	require.NoError(t, reversed.Validate())

	// A distinct statement ordered by a column outside its projections
	// validates in both call orders: rasql renders it and leaves the
	// PostgreSQL/MySQL refusal to the server.
	distinctFirst, err := query.NewSelect(users, email)
	require.NoError(t, err)
	distinctFirst, err = distinctFirst.WithDistinct()
	require.NoError(t, err)
	distinctFirst, err = distinctFirst.WithOrder(query.Asc(userID))
	require.NoError(t, err)
	require.NoError(t, distinctFirst.Validate())

	orderFirst, err := query.NewSelect(users, email)
	require.NoError(t, err)
	orderFirst, err = orderFirst.WithOrder(query.Asc(userID))
	require.NoError(t, err)
	orderFirst, err = orderFirst.WithDistinct()
	require.NoError(t, err)
	require.NoError(t, orderFirst.Validate())
}

// TestFunctionAcceptsDistinctArgument proves WithDistinct marks a function
// call as COUNT(DISTINCT x)-shaped without changing its name or arguments,
// and that combining it with CountAll's star is refused, since
// COUNT(DISTINCT *) is not legal SQL.
func TestFunctionAcceptsDistinctArgument(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")

	plain := query.Count(userID)
	require.False(t, plain.Distinct())

	distinct := plain.WithDistinct()
	require.True(t, distinct.Distinct())
	require.Equal(t, plain.Name(), distinct.Name())
	require.Equal(t, plain.Arguments(), distinct.Arguments())
	require.False(t, plain.Distinct(), "WithDistinct must not mutate the receiver")

	statement, err := query.NewSelect(users, distinct.As("distinct_count"))
	require.NoError(t, err)
	require.NoError(t, statement.Validate())

	_, err = query.NewSelect(users, query.CountAll().WithDistinct())
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "COUNT(DISTINCT *) is not valid")
}

// TestFunctionDistinctFollowsTheFunctionClass proves WithDistinct is judged by
// what the called function is rather than by the modifier alone: an aggregate
// takes it, a curated scalar refuses it because DISTINCT asks a function to
// combine one row per distinct argument value and only an aggregate combines
// rows, and Func carries it through unchecked because rasql does not know
// whether the named function aggregates.
func TestFunctionDistinctFollowsTheFunctionClass(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")
	email := users.Column("email")

	// WithDistinct changes where a call may sit no more than it changes what
	// the call is, so it leaves Aggregates alone on either side of the split.
	require.True(t, query.Count(userID).WithDistinct().Aggregates())
	require.False(t, query.Lower(email).WithDistinct().Aggregates())
	require.False(t, query.Func("SUM", userID).WithDistinct().Aggregates())

	refused := map[string]query.Function{
		"Lower":    query.Lower(email).WithDistinct(),
		"Upper":    query.Upper(email).WithDistinct(),
		"Abs":      query.Abs(userID).WithDistinct(),
		"Coalesce": query.Coalesce(email, query.Bind("")).WithDistinct(),
		"Call":     query.Call(query.FunctionLower, email).WithDistinct(),
	}
	for name, function := range refused {
		t.Run(name, func(t *testing.T) {
			_, err := query.NewSelect(users, function.As("value"))
			requireQueryValidationError(t, err)
			require.ErrorContains(t, err, "does not aggregate, so it does not support DISTINCT")
		})
	}

	// An uncurated aggregate is reachable only through Func, and DISTINCT is
	// part of how it is called, so validation admits the pair and leaves the
	// target database to judge the name.
	statement, err := query.NewSelect(users, query.Func("group_concat", email).WithDistinct().As("tags"))
	require.NoError(t, err)
	require.NoError(t, statement.Validate())

	// A Func call stays scalar with the modifier attached, so it reaches a
	// WHERE clause where an aggregate would be refused.
	statement, err = query.NewSelect(users, userID)
	require.NoError(t, err)
	_, err = statement.WithWhere(query.Equal(query.Func("any_value", email).WithDistinct(), query.Bind("a")))
	require.NoError(t, err)
}

func TestFunctionConstructorsCarryTheirCall(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")
	email := users.Column("email")

	tests := map[string]struct {
		function   query.Function
		name       query.FunctionName
		arguments  []query.Expression
		star       bool
		aggregates bool
	}{
		"Count":    {function: query.Count(userID), name: query.FunctionCount, arguments: []query.Expression{userID}, aggregates: true},
		"CountAll": {function: query.CountAll(), name: query.FunctionCount, star: true, aggregates: true},
		"Sum":      {function: query.Sum(userID), name: query.FunctionSum, arguments: []query.Expression{userID}, aggregates: true},
		"Min":      {function: query.Min(userID), name: query.FunctionMin, arguments: []query.Expression{userID}, aggregates: true},
		"Max":      {function: query.Max(userID), name: query.FunctionMax, arguments: []query.Expression{userID}, aggregates: true},
		"Avg":      {function: query.Avg(userID), name: query.FunctionAvg, arguments: []query.Expression{userID}, aggregates: true},
		"Call":     {function: query.Call(query.FunctionSum, userID), name: query.FunctionSum, arguments: []query.Expression{userID}, aggregates: true},
		"Coalesce": {function: query.Coalesce(email, query.Bind("")), name: query.FunctionCoalesce, arguments: []query.Expression{email, query.Bind("")}},
		"Lower":    {function: query.Lower(email), name: query.FunctionLower, arguments: []query.Expression{email}},
		"Upper":    {function: query.Upper(email), name: query.FunctionUpper, arguments: []query.Expression{email}},
		"Abs":      {function: query.Abs(userID), name: query.FunctionAbs, arguments: []query.Expression{userID}},
		"Func":     {function: query.Func("jsonb_path_query", userID), name: query.FunctionName("jsonb_path_query"), arguments: []query.Expression{userID}},
		// A Func call is always scalar, so Aggregates reports false even when
		// the caller-supplied name collides with a curated aggregate.
		"FuncNamedAfterAggregate": {function: query.Func("SUM", userID), name: query.FunctionName("SUM"), arguments: []query.Expression{userID}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, test.name, test.function.Name())
			require.Equal(t, test.arguments, test.function.Arguments())
			require.Equal(t, test.star, test.function.Star())
			require.Equal(t, test.aggregates, test.function.Aggregates())
		})
	}

	function := query.Sum(userID)
	arguments := function.Arguments()
	arguments[0] = nil
	require.Equal(t, userID, function.Arguments()[0], "mutating the returned slice must not change the expression")
}

func TestSelectRejectsInvalidStatements(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")

	_, err = query.NewSelect(users)
	requireQueryValidationError(t, err)

	statement, err := query.NewSelect(users, userID)
	require.NoError(t, err)

	_, err = statement.WithWhere(query.And(userID))
	requireQueryValidationError(t, err)

	_, err = statement.WithLimit(-1)
	requireQueryValidationError(t, err)

	other, err := query.NewTableRef(ordersTable())
	require.NoError(t, err)
	otherID := other.Column("id")
	_, err = statement.WithWhere(query.Equal(otherID, query.Bind(1)))
	requireQueryValidationError(t, err)

	_, err = statement.WithWhere(query.In(userID))
	requireQueryValidationError(t, err)

	// nil binds as a bound NULL, so it is accepted in a membership value list.
	inWithNil := query.In(userID, query.Bind(1), nil)
	_, err = statement.WithWhere(inWithNil)
	require.NoError(t, err)
	require.Equal(t, []query.Expression{query.Bind(1), query.Bind(nil)}, inWithNil.Values())

	// nil is still refused in a slot that stays Expression-typed.
	_, err = statement.WithWhere(query.And(query.Equal(userID, 1), nil))
	requireQueryValidationError(t, err)

	_, err = statement.WithWhere(query.In(otherID, query.Bind(1)))
	requireQueryValidationError(t, err)

	// LENGTH is deliberately excluded from the curated set: PostgreSQL, MySQL,
	// and SQLite disagree on whether it counts characters or bytes, so
	// Call refuses it rather than hiding that difference.
	_, err = query.NewSelect(users, query.Call("LENGTH", userID))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, `unsupported function "LENGTH"`)

	_, err = statement.WithWhere(query.GreaterThan(userID, query.Scalar(query.Select{})))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "from")

	// The subquery reads another table, so the projection count is what it is
	// refused for. A second unaliased users in the subquery would be refused
	// first, as a source the enclosing statement already answers for.
	twoProjections, err := query.NewSelect(other, otherID, query.Project(query.Bind(1)))
	require.NoError(t, err)
	_, err = statement.WithWhere(query.GreaterThan(userID, query.Scalar(twoProjections)))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "must select exactly one expression, got 2")

	_, err = statement.WithWhere(query.In(userID, query.Scalar(twoProjections)))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "use InSelect or NotInSelect")

	// A subquery correlates only when it says so. WithCorrelation names the
	// enclosing table, and a statement that has not named one reads no table
	// outside its own FROM and joins, so this hits the same "outside the
	// statement" error the table-scope check reports for any expression, while
	// the statement is still being built and long before it is nested.
	correlated, err := query.NewSelect(other, otherID)
	require.NoError(t, err)
	_, err = correlated.WithWhere(query.Equal(userID, query.Bind(1)))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "references table")
	require.ErrorContains(t, err, "outside the statement")

	_, err = query.NewSelect(users, query.Call(query.FunctionSum))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "takes exactly one argument, got 0")

	_, err = query.NewSelect(users, query.Call(query.FunctionCount, userID, userID))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "takes exactly one argument, got 2")

	_, err = query.NewSelect(users, query.Count(otherID))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "references table")
	require.ErrorContains(t, err, "outside the statement")

	statement, err = query.NewSelect(users,
		query.CountAll(),
		query.Max(userID),
	)
	require.NoError(t, err)
	require.NoError(t, statement.Validate())
}

func TestSelectAcceptsMembershipPredicate(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")
	statement, err := query.NewSelect(users, userID)
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
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")
	orders, err := query.NewTableRef(ordersTable())
	require.NoError(t, err)
	orderID := orders.Column("id")
	orderUserID := orders.Column("user_id")
	amount := orders.Column("amount")

	statement, err := query.NewSelect(users, userID)
	require.NoError(t, err)

	orderedUserIDs, err := query.NewSelect(orders, orderUserID)
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.InSelect(userID, orderedUserIDs))
	require.NoError(t, err)
	require.NoError(t, statement.Validate())

	averageAmount, err := query.NewSelect(orders, query.Avg(amount))
	require.NoError(t, err)
	require.NoError(t, statement.Validate())
	statement, err = statement.WithWhere(query.GreaterThanOrEqual(userID, query.Scalar(averageAmount)))
	require.NoError(t, err)
	require.NoError(t, statement.Validate())

	notInStatement, err := query.NewSelect(users, userID)
	require.NoError(t, err)
	notInStatement, err = notInStatement.WithWhere(query.NotInSelect(userID, orderedUserIDs))
	require.NoError(t, err)
	require.NoError(t, notInStatement.Validate())

	// A subquery two levels deep: the outer statement's InSelect reads a
	// statement whose own WHERE runs another InSelect. The innermost statement
	// aliases orders, which the statement enclosing it already reads: two
	// unaliased orders in nested scopes render column references a server would
	// answer from the inner one alone, which validation refuses.
	innerOrders, err := orders.As("inner_orders")
	require.NoError(t, err)
	innermost, err := query.NewSelect(innerOrders, innerOrders.Column("id"))
	require.NoError(t, err)
	innermost, err = innermost.WithWhere(query.GreaterThan(innerOrders.Column("amount"), query.Bind(10)))
	require.NoError(t, err)
	nested, err := query.NewSelect(orders, orderUserID)
	require.NoError(t, err)
	nested, err = nested.WithWhere(query.InSelect(orderID, innermost))
	require.NoError(t, err)
	deep, err := query.NewSelect(users, userID)
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
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")
	orders, err := query.NewTableRef(ordersTable())
	require.NoError(t, err)
	orderUserID := orders.Column("user_id")

	base, err := query.NewSelect(users, userID)
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
				aggregated, err := query.NewSelect(users, query.CountAll())
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
				aggregated, err := query.NewSelect(users, query.CountAll())
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
				aggregated, err := query.NewSelect(users, query.CountAll())
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
				_, err := query.NewSelect(users, query.Sum(query.Sum(userID)))
				return err
			},
			message: `calls aggregate function "SUM" inside another aggregate function`,
		},
		"aggregate nested below an operator": {
			build: func() error {
				_, err := query.NewSelect(users, query.Max(query.GreaterThan(query.Count(userID), query.Bind(1))))
				return err
			},
			message: `calls aggregate function "COUNT" inside another aggregate function`,
		},
		"column projected beside an aggregate": {
			build: func() error {
				_, err := query.NewSelect(users, userID, query.CountAll())
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
		"group by": {
			build: func() error {
				_, err := query.NewGroupedSelect(users, []query.Expression{query.Count(userID)}, userID)
				return err
			},
			message: `calls aggregate function "COUNT" in a GROUP BY clause`,
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
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")
	email := users.Column("email")
	orders, err := query.NewTableRef(ordersTable())
	require.NoError(t, err)
	orderUserID := orders.Column("user_id")

	// An aggregate-only projection set filters and joins by columns, because
	// WHERE and JOIN ON run before aggregation on the source rows.
	statement, err := query.NewSelect(users,
		query.CountAll().As("total"),
		query.Max(userID).As("top"),
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
	columns, err := query.NewSelect(users, userID, email)
	require.NoError(t, err)
	columns, err = columns.WithOrder(query.Asc(email))
	require.NoError(t, err)
	require.NoError(t, columns.Validate())
}

// TestSelectAcceptsScalarFunctions covers every clause a scalar function call
// reaches through carrying the expression context unchanged: a projection, a
// WHERE clause, a JOIN ON condition, a GROUP BY clause, an ORDER BY clause,
// and a HAVING clause of a statement that groups.
func TestSelectAcceptsScalarFunctions(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")
	email := users.Column("email")
	orders, err := query.NewTableRef(ordersTable())
	require.NoError(t, err)
	orderUserID := orders.Column("user_id")

	// A scalar call in a projection needs no GROUP BY, unlike an aggregate
	// beside a bare column.
	projected, err := query.NewSelect(users, query.Lower(email).As("lower_email"))
	require.NoError(t, err)
	require.NoError(t, projected.Validate())

	// A scalar call in WHERE, which an aggregate call is refused in.
	filtered, err := query.NewSelect(users, userID)
	require.NoError(t, err)
	filtered, err = filtered.WithWhere(query.Equal(query.Lower(email), query.Bind("ada@example.com")))
	require.NoError(t, err)
	require.NoError(t, filtered.Validate())

	// A scalar call in a JOIN ON condition.
	joined, err := query.NewSelect(users, userID)
	require.NoError(t, err)
	joined, err = joined.WithJoin(query.InnerJoin(orders, query.Equal(userID, query.Abs(orderUserID))))
	require.NoError(t, err)
	require.NoError(t, joined.Validate())

	// A scalar call in a GROUP BY clause.
	grouped, err := query.NewGroupedSelect(users, []query.Expression{query.Lower(email)}, query.Lower(email))
	require.NoError(t, err)
	require.NoError(t, grouped.Validate())

	// A scalar call in an ORDER BY clause of an ungrouped, non-aggregating
	// statement.
	ordered, err := query.NewSelect(users, userID)
	require.NoError(t, err)
	ordered, err = ordered.WithOrder(query.Asc(query.Lower(email)))
	require.NoError(t, err)
	require.NoError(t, ordered.Validate())

	// A scalar call in a HAVING clause of a statement that groups.
	having, err := query.NewGroupedSelect(users, []query.Expression{email}, email)
	require.NoError(t, err)
	having, err = having.WithHaving(query.NotEqual(query.Lower(email), query.Bind("done")))
	require.NoError(t, err)
	require.NoError(t, having.Validate())
}

// TestSelectRejectsInvalidScalarCalls covers the arity and name checks a
// scalar call has to pass, and pins that a star call is refused for a scalar
// name just as it is for a non-COUNT aggregate.
func TestSelectRejectsInvalidScalarCalls(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")
	email := users.Column("email")

	tests := map[string]struct {
		call    query.Expression
		message string
	}{
		"Coalesce with one argument": {
			call:    query.Coalesce(email),
			message: `function "COALESCE" takes at least 2 argument`,
		},
		"Call(FunctionCoalesce) with none": {
			call:    query.Call(query.FunctionCoalesce),
			message: `function "COALESCE" takes at least 2 argument`,
		},
		"Lower with two arguments": {
			call:    query.Call(query.FunctionLower, email, email),
			message: `function "LOWER" takes exactly 1 argument`,
		},
		"unknown name": {
			call:    query.Call(query.FunctionName("LENGTH"), email),
			message: `unsupported function "LENGTH"`,
		},
		"Upper with two arguments": {
			call:    query.Call(query.FunctionUpper, userID, userID),
			message: `function "UPPER" takes exactly 1 argument`,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := query.NewSelect(users, query.Project(test.call))
			requireQueryValidationError(t, err)
			require.ErrorContains(t, err, test.message)
		})
	}
}

// TestSelectPlacesAggregatesInsideScalarFunctions pins the placement rules
// that fall out of carrying the expression context unchanged through a scalar
// call: an aggregate nested inside one is judged exactly as if it sat in the
// scalar call's place directly.
func TestSelectPlacesAggregatesInsideScalarFunctions(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")
	orders, err := query.NewTableRef(ordersTable())
	require.NoError(t, err)
	orderUserID := orders.Column("user_id")

	// COALESCE(SUM(x), 0) is accepted in a projection: the nested aggregate
	// sees allowsAggregate true at depth 0.
	projected, err := query.NewSelect(users, query.Coalesce(query.Sum(userID), query.Bind(0)).As("total"))
	require.NoError(t, err)
	require.NoError(t, projected.Validate())

	// The same call is refused in WHERE: the nested aggregate sees
	// allowsAggregate false.
	base, err := query.NewSelect(users, userID)
	require.NoError(t, err)
	_, err = base.WithWhere(query.GreaterThan(query.Coalesce(query.Sum(userID), query.Bind(0)), query.Bind(0)))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, `calls aggregate function "SUM" in a WHERE clause`)

	// The same call is refused in a JOIN ON condition, for the same reason.
	_, err = base.WithJoin(query.InnerJoin(orders, query.Equal(userID, query.Coalesce(query.Sum(orderUserID), query.Bind(0)))))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, `calls aggregate function "SUM" in a JOIN ON condition`)

	// SUM(COALESCE(x, 0)) is accepted: the aggregate walks its arguments one
	// level deeper into aggregate nesting, and a scalar call there is fine.
	sumOfCoalesce, err := query.NewSelect(users, query.Sum(query.Coalesce(userID, query.Bind(0))).As("total"))
	require.NoError(t, err)
	require.NoError(t, sumOfCoalesce.Validate())

	// SUM(COALESCE(SUM(x), 0)) is refused: the inner SUM sees aggregateDepth
	// 1 through the scalar call that carries it unchanged.
	_, err = query.NewSelect(users, query.Sum(query.Coalesce(query.Sum(userID), query.Bind(0))))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, `calls aggregate function "SUM" inside another aggregate function`)

	// LOWER(email) counts as a bare column read, exactly as email would on its
	// own: it is refused beside CountAll() in an ungrouped projection set and
	// accepted once the statement groups.
	email := users.Column("email")
	_, err = query.NewSelect(users, query.CountAll(), query.Lower(email))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "reads a column outside an aggregate function")

	grouped, err := query.NewGroupedSelect(users, []query.Expression{email}, query.CountAll(), query.Lower(email))
	require.NoError(t, err)
	require.NoError(t, grouped.Validate())
}

// TestFuncValidatesEscapeHatchName pins that Func checks its caller-supplied
// name only for being a legal identifier, reusing schema.ValidateIdentifier,
// and that its call is always scalar: it reaches a WHERE clause even when the
// name reuses a curated aggregate name such as SUM, because Func never routes
// through the aggregate placement rules.
func TestFuncValidatesEscapeHatchName(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")
	email := users.Column("email")

	statement, err := query.NewSelect(users, query.Func("jsonb_path_query", email, query.Bind("$.a")).As("path"))
	require.NoError(t, err)
	require.NoError(t, statement.Validate())

	base, err := query.NewSelect(users, userID)
	require.NoError(t, err)
	filtered, err := base.WithWhere(query.Equal(query.Func("SUM", userID), query.Bind(1)))
	require.NoError(t, err)
	require.NoError(t, filtered.Validate())
	// Aggregates has to agree with the placement rule validation just applied.
	require.False(t, query.Func("SUM", userID).Aggregates())

	_, err = query.NewSelect(users, query.Func("bad-name", userID))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, `invalid function name "bad-name"`)

	_, err = query.NewSelect(users, query.Func("", userID))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, `invalid function name ""`)
}

// TestSelectGroupsAndFilters covers the basic shape of a grouped statement:
// NewGroupedSelect accepts a mixed projection set once a grouping is supplied,
// WithGroupBy refines an already-valid ungrouped statement, and WithHaving sets
// and then replaces the grouped predicate.
func TestSelectGroupsAndFilters(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")
	email := users.Column("email")

	grouped, err := query.NewGroupedSelect(users, []query.Expression{email},
		email,
		query.CountAll().As("total"),
	)
	require.NoError(t, err)
	require.Nil(t, grouped.Having())

	keys := grouped.GroupBy()
	require.Equal(t, []query.Expression{email}, keys)
	keys[0] = nil
	require.Equal(t, []query.Expression{email}, grouped.GroupBy(), "mutating the returned slice must not change the statement")

	aggregateOnly, err := query.NewSelect(users, query.CountAll())
	require.NoError(t, err)
	refined, err := aggregateOnly.WithGroupBy(userID)
	require.NoError(t, err)
	require.Equal(t, []query.Expression{userID}, refined.GroupBy())

	withHaving, err := refined.WithHaving(query.GreaterThan(query.CountAll(), query.Bind(1)))
	require.NoError(t, err)
	require.Equal(t, query.Expression(query.GreaterThan(query.CountAll(), query.Bind(1))), withHaving.Having())

	replaced, err := withHaving.WithHaving(query.LessThan(query.CountAll(), query.Bind(10)))
	require.NoError(t, err)
	require.Equal(t, query.Expression(query.LessThan(query.CountAll(), query.Bind(10))), replaced.Having())
}

// TestJoinedSelectGroupsByJoinedColumn covers what NewJoinedSelect adds over
// NewGroupedSelect: the joins are present at the first validation, so a grouping
// expression and a projection may read a joined table. WithJoin cannot reach the
// same statement, because validation refuses the grouping before the join is
// attached.
func TestJoinedSelectGroupsByJoinedColumn(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")
	orders, err := query.NewTableRef(ordersTable())
	require.NoError(t, err)
	orderUserID := orders.Column("user_id")
	join := query.InnerJoin(orders, query.Equal(userID, orderUserID))

	grouped, err := query.NewJoinedSelect(users, []query.Join{join}, []query.Expression{orderUserID},
		orderUserID,
		query.CountAll().As("total"),
	)
	require.NoError(t, err)
	require.Equal(t, []query.Expression{orderUserID}, grouped.GroupBy())
	require.Equal(t, []query.Join{join}, grouped.Joins())

	// A nil grouping leaves the statement ungrouped, which is where the
	// projection of a joined column also needs the join up front.
	ungrouped, err := query.NewJoinedSelect(users, []query.Join{join}, nil,
		userID,
		orderUserID,
	)
	require.NoError(t, err)
	require.Empty(t, ungrouped.GroupBy())

	// The joins the caller passes are copied, so a later write to their slice
	// must not reach the statement.
	joins := []query.Join{join}
	copied, err := query.NewJoinedSelect(users, joins, nil, userID)
	require.NoError(t, err)
	joins[0] = query.Join{}
	require.Equal(t, []query.Join{join}, copied.Joins())

	// Without the join the same grouping is refused, which is what attaching
	// the joins afterwards amounted to.
	_, err = query.NewJoinedSelect(users, nil, []query.Expression{orderUserID}, query.CountAll())
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, `references table "orders" outside the statement`)
}

// TestSelectRejectsInvalidGrouping covers the rules a GROUP BY expression has to
// follow: no aggregate, no table outside the statement, and no bare bound value.
func TestSelectRejectsInvalidGrouping(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")
	orders, err := query.NewTableRef(ordersTable())
	require.NoError(t, err)
	orderUserID := orders.Column("user_id")

	_, err = query.NewGroupedSelect(users, []query.Expression{query.CountAll()}, userID)
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, `calls aggregate function "COUNT" in a GROUP BY clause`)

	_, err = query.NewGroupedSelect(users, []query.Expression{orderUserID}, userID)
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "references table")
	require.ErrorContains(t, err, "outside the statement")

	_, err = query.NewGroupedSelect(users, []query.Expression{query.Bind(1)}, userID)
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "must not be a bound value")

	// A grouping expression that merely contains a bound value, rather than
	// being one, is a real grouping key and stays legal.
	nested, err := query.NewGroupedSelect(users, []query.Expression{query.GreaterThan(userID, query.Bind(3))}, userID)
	require.NoError(t, err)
	require.NoError(t, nested.Validate())

	// NewGroupedSelect with no grouping keys behaves exactly like NewSelect: it
	// leaves the statement ungrouped, so a mixed projection set is refused with
	// the identical message NewSelect gives, rather than being silently
	// accepted because the call went through the grouped constructor.
	_, errFromNewSelect := query.NewSelect(users, userID, query.CountAll())
	requireQueryValidationError(t, errFromNewSelect)
	_, errFromEmptyGroup := query.NewGroupedSelect(users, nil, userID, query.CountAll())
	requireQueryValidationError(t, errFromEmptyGroup)
	require.Equal(t, errFromNewSelect.Error(), errFromEmptyGroup.Error())
}

// TestSelectRejectsInvalidHaving covers the rules that make a HAVING clause
// legal: it needs a statement that groups, either explicitly or through a
// projection set that aggregates and reads no column outside an aggregate, and
// follows the same aggregate-placement rules as every other clause.
func TestSelectRejectsInvalidHaving(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")
	email := users.Column("email")
	orders, err := query.NewTableRef(ordersTable())
	require.NoError(t, err)
	orderUserID := orders.Column("user_id")

	plain, err := query.NewSelect(users, userID)
	require.NoError(t, err)
	_, err = plain.WithHaving(query.GreaterThan(userID, query.Bind(1)))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "requires a GROUP BY clause or a projection set that aggregates")

	aggregateOnly, err := query.NewSelect(users, query.CountAll())
	require.NoError(t, err)
	_, err = aggregateOnly.WithHaving(userID)
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "reads a column outside an aggregate function while the projections aggregate, which requires a GROUP BY clause")

	_, err = aggregateOnly.WithHaving(query.GreaterThan(query.Count(orderUserID), query.Bind(1)))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "references table")
	require.ErrorContains(t, err, "outside the statement")

	_, err = aggregateOnly.WithHaving(query.GreaterThan(query.Max(query.Max(userID)), query.Bind(1)))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, `calls aggregate function "MAX" inside another aggregate function`)

	grouped, err := query.NewGroupedSelect(users, []query.Expression{email}, email, query.CountAll())
	require.NoError(t, err)
	withHaving, err := grouped.WithHaving(query.NotEqual(email, query.Bind("done")))
	require.NoError(t, err)
	require.NoError(t, withHaving.Validate())
}

// TestSelectAcceptsGroupedStatements pins what must keep validating once a
// statement groups, the counterpart to TestSelectAcceptsWellPlacedAggregates.
func TestSelectAcceptsGroupedStatements(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	email := users.Column("email")

	// A grouped statement may mix a bare column with an aggregate.
	statement, err := query.NewGroupedSelect(users, []query.Expression{email},
		email,
		query.CountAll().As("total"),
	)
	require.NoError(t, err)
	require.NoError(t, statement.Validate())

	// A grouped HAVING may read a bare column.
	withHaving, err := statement.WithHaving(query.NotEqual(email, query.Bind("done")))
	require.NoError(t, err)
	require.NoError(t, withHaving.Validate())

	// A grouped ORDER BY may read a bare column.
	withOrder, err := statement.WithOrder(query.Asc(email))
	require.NoError(t, err)
	require.NoError(t, withOrder.Validate())

	// HAVING over an aggregate needs no GROUP BY when the projection set
	// aggregates and reads no column outside an aggregate, because that set is
	// already one group.
	aggregateOnly, err := query.NewSelect(users, query.CountAll())
	require.NoError(t, err)
	aggregateHaving, err := aggregateOnly.WithHaving(query.GreaterThan(query.CountAll(), query.Bind(1)))
	require.NoError(t, err)
	require.NoError(t, aggregateHaving.Validate())

	// Not every projection in that set has to aggregate. A projection that
	// reads no column, a bound value here, sits beside the aggregate and the
	// set still counts as one group, so the HAVING stays legal.
	besideBoundValue, err := query.NewSelect(users, query.CountAll(), query.Project(query.Bind(7)))
	require.NoError(t, err)
	boundValueHaving, err := besideBoundValue.WithHaving(query.GreaterThan(query.CountAll(), query.Bind(1)))
	require.NoError(t, err)
	require.NoError(t, boundValueHaving.Validate())
}

// TestSelectAcceptsSubqueriesInGroupedClauses pins that GROUP BY and HAVING are
// SELECT clauses like the others, so a subquery is legal in both. GROUP BY is
// validated through validateSubqueryClauseExpression and HAVING through
// aggregateClauseContext, and both of those permit a subquery, which keeps the
// clause list in the misplaced-subquery message honest.
func TestSelectAcceptsSubqueriesInGroupedClauses(t *testing.T) {
	orders, err := query.NewTableRef(ordersTable())
	require.NoError(t, err)
	amount := orders.Column("amount")
	// A separate alias keeps the average its own scope rather than a
	// correlation, which this package does not support.
	allOrders, err := orders.As("all_orders")
	require.NoError(t, err)
	allAmount := allOrders.Column("amount")
	averageAmount, err := query.NewSelect(allOrders, query.Avg(allAmount))
	require.NoError(t, err)

	// GROUP BY groups on whether each amount beats the average.
	aboveAverage := query.GreaterThan(amount, query.Scalar(averageAmount))
	grouped, err := query.NewGroupedSelect(orders, []query.Expression{aboveAverage}, query.CountAll())
	require.NoError(t, err)
	require.NoError(t, grouped.Validate())

	// HAVING compares an aggregate against a scalar subquery.
	withHaving, err := grouped.WithHaving(query.GreaterThan(query.Avg(amount), query.Scalar(averageAmount)))
	require.NoError(t, err)
	require.NoError(t, withHaving.Validate())
}

// TestSelectJudgesAggregatesInsideMembership pins how the clause-aware walk
// treats a membership test. The test itself is an ordinary predicate, so the
// tested expression and every member of its value list are judged by the rule
// that governs the clause the test sits in, exactly as the same operand would be
// outside an IN.
func TestSelectJudgesAggregatesInsideMembership(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID := users.Column("id")
	email := users.Column("email")

	base, err := query.NewSelect(users, userID)
	require.NoError(t, err)
	aggregated, err := query.NewSelect(users, query.CountAll())
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
				_, err := query.NewSelect(users, query.Max(query.In(userID, query.Count(email))))
				return err
			},
			message: `calls aggregate function "COUNT" inside another aggregate function`,
		},
		"column in a value list beside an aggregate projection": {
			build: func() error {
				_, err := query.NewSelect(users,
					query.Project(query.In(email, query.Bind("ada@example.com"))),
					query.CountAll(),
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
			_, err := query.NewSelect(users, query.Count(query.In(userID, query.Bind(1), query.Bind(2))))
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

func TestTableRefRejectsUnknownColumn(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)

	err = users.Column("missing").Validate()
	require.Error(t, err)
}

func TestTableRefCopiesDescriptor(t *testing.T) {
	descriptor := usersTable()
	users, err := query.NewTableRef(descriptor)
	require.NoError(t, err)

	descriptor.Columns[0].Name = "changed"
	descriptor.PrimaryKey[0] = "changed"

	require.Equal(t, []string{"id"}, users.Definition().PrimaryKey)
}

func TestMustTableRef(t *testing.T) {
	require.Panics(t, func() {
		query.MustTableRef(schema.TableDef{})
	})
}

// TestTableRefReportsItsSchema pins Schema, QualifierSchema and Qualifier for
// an unqualified table, a qualified unaliased table and a qualified aliased
// table: an alias replaces the whole qualified name, so QualifierSchema
// returns "" once a table is aliased even though Schema still reports it.
func TestTableRefReportsItsSchema(t *testing.T) {
	unqualified := query.MustTableRef(usersTable())
	require.Equal(t, "", unqualified.Schema())
	require.Equal(t, "", unqualified.QualifierSchema())
	require.Equal(t, "users", unqualified.Qualifier())

	descriptor := usersTable()
	descriptor.Schema = "audit"
	qualified := query.MustTableRef(descriptor)
	require.Equal(t, "audit", qualified.Schema())
	require.Equal(t, "audit", qualified.QualifierSchema())
	require.Equal(t, "users", qualified.Qualifier())
	require.Equal(t, "audit.users", qualified.QualifiedName())

	aliased, err := qualified.As("u")
	require.NoError(t, err)
	require.Equal(t, "audit", aliased.Schema())
	require.Equal(t, "", aliased.QualifierSchema())
	require.Equal(t, "u", aliased.Qualifier())
	require.Equal(t, "u", aliased.QualifiedName())
}

// TestSelectJoinsSameNameTablesFromDifferentSchemas pins the fix to key():
// two same-named tables in different schemas are distinct join sources, a
// case Select.Validate rejected as a duplicate before the schema joined the
// key.
func TestSelectJoinsSameNameTablesFromDifferentSchemas(t *testing.T) {
	tenantADescriptor := usersTable()
	tenantADescriptor.Schema = "tenant_a"
	tenantA, err := query.NewTableRef(tenantADescriptor)
	require.NoError(t, err)

	tenantBDescriptor := usersTable()
	tenantBDescriptor.Schema = "tenant_b"
	tenantB, err := query.NewTableRef(tenantBDescriptor)
	require.NoError(t, err)

	tenantAID := tenantA.Column("id")
	tenantBID := tenantB.Column("id")

	join := query.InnerJoin(tenantB, query.Equal(tenantAID, tenantBID))
	_, err = query.NewJoinedSelect(tenantA, []query.Join{join}, nil, tenantAID)
	require.NoError(t, err)
}

// TestSelectNamesQualifiedTableInDuplicateSourceError pins the wording change
// at query/select.go:300: joining a qualified table to itself still errors,
// and the message names the qualified table rather than the bare name.
func TestSelectNamesQualifiedTableInDuplicateSourceError(t *testing.T) {
	descriptor := usersTable()
	descriptor.Schema = "audit"
	from, err := query.NewTableRef(descriptor)
	require.NoError(t, err)
	other, err := query.NewTableRef(descriptor)
	require.NoError(t, err)

	fromID := from.Column("id")
	otherID := other.Column("id")

	join := query.InnerJoin(other, query.Equal(fromID, otherID))
	_, err = query.NewJoinedSelect(from, []query.Join{join}, nil, fromID)
	require.ErrorContains(t, err, `duplicates table reference "audit.users"`)
}

// TestTableRefReportsWhetherItIsQualified pins Qualified on query.TableRef against
// the same method on the descriptor it wraps. Qualified describes the table,
// so an alias leaves it alone even though the alias empties QualifierSchema.
func TestTableRefReportsWhetherItIsQualified(t *testing.T) {
	unqualified := query.MustTableRef(usersTable())
	require.False(t, unqualified.Qualified())

	descriptor := usersTable()
	descriptor.Schema = "audit"
	qualified := query.MustTableRef(descriptor)
	require.True(t, qualified.Qualified())

	aliased, err := qualified.As("u")
	require.NoError(t, err)
	require.True(t, aliased.Qualified())
	require.Equal(t, "", aliased.QualifierSchema())
}

// TestTableRefNamesQualifiedTableInUnknownColumnError pins the fifth
// table-naming message: ColumnRef.Validate names the qualified table, and the
// alias once the table is aliased, exactly as every other message does.
func TestTableRefNamesQualifiedTableInUnknownColumnError(t *testing.T) {
	descriptor := usersTable()
	descriptor.Schema = "tenant"
	qualified := query.MustTableRef(descriptor)

	err := qualified.Column("missing").Validate()
	require.ErrorContains(t, err, `table "tenant.users" has no column "missing"`)

	aliased, err := qualified.As("u")
	require.NoError(t, err)
	err = aliased.Column("missing").Validate()
	require.ErrorContains(t, err, `table "u" has no column "missing"`)
}

// TestSelectRejectsSourcesThatShareOneName covers the rule that two sources of
// one statement must render under names a server can tell apart. It is a check
// of its own, not a side effect of the table key: every pair below holds two
// distinct keys, and each renders SQL SQLite rejects with "ambiguous column
// name". TestSQLiteRefusesAmbiguousSources runs both sets against a real
// database.
func TestSelectRejectsSourcesThatShareOneName(t *testing.T) {
	qualifiedUsers := func(schemaName string) schema.TableDef {
		descriptor := usersTable()
		descriptor.Schema = schemaName
		return descriptor
	}
	aliasOf := func(t *testing.T, descriptor schema.TableDef, alias string) query.TableRef {
		t.Helper()
		table, err := query.MustTableRef(descriptor).As(alias)
		require.NoError(t, err)
		return table
	}

	rejected := map[string]struct {
		from    func(*testing.T) query.TableRef
		joined  func(*testing.T) query.TableRef
		message string
	}{
		// The finding this PR introduced: two different qualified tables under
		// one alias.
		"two qualified tables share an alias": {
			from:    func(t *testing.T) query.TableRef { return aliasOf(t, qualifiedUsers("tenant_a"), "u") },
			joined:  func(t *testing.T) query.TableRef { return aliasOf(t, qualifiedUsers("tenant_b"), "u") },
			message: `table "tenant_b.users" is referred to as "u", which already refers to table "tenant_a.users"`,
		},
		// The hole that predates this PR: the key never caught an alias clash
		// between two tables that differed in any other way.
		"two unrelated tables share an alias": {
			from:    func(t *testing.T) query.TableRef { return aliasOf(t, usersTable(), "u") },
			joined:  func(t *testing.T) query.TableRef { return aliasOf(t, ordersTable(), "u") },
			message: `table "orders" is referred to as "u", which already refers to table "users"`,
		},
		// The regression this PR introduced: a bare "users" names the
		// unqualified table and the qualified one equally, in either order.
		"unqualified table joined to a qualified one of the same name": {
			from:    func(t *testing.T) query.TableRef { return query.MustTableRef(usersTable()) },
			joined:  func(t *testing.T) query.TableRef { return query.MustTableRef(qualifiedUsers("tenant_a")) },
			message: `table "tenant_a.users" is referred to as "users", which already refers to table "users"`,
		},
		"qualified table joined to an unqualified one of the same name": {
			from:    func(t *testing.T) query.TableRef { return query.MustTableRef(qualifiedUsers("tenant_a")) },
			joined:  func(t *testing.T) query.TableRef { return query.MustTableRef(usersTable()) },
			message: `table "users" is referred to as "users", which already refers to table "tenant_a.users"`,
		},
		// An alias that repeats an unaliased source's own name is the same
		// clash reached from the other side.
		"an alias repeats an unaliased source's name": {
			from:    func(t *testing.T) query.TableRef { return query.MustTableRef(usersTable()) },
			joined:  func(t *testing.T) query.TableRef { return aliasOf(t, ordersTable(), "users") },
			message: `table "orders" is referred to as "users", which already refers to table "users"`,
		},
	}
	for name, testCase := range rejected {
		t.Run(name, func(t *testing.T) {
			from := testCase.from(t)
			joined := testCase.joined(t)
			// The table key is schema, name and alias together, so a pair that
			// differs in any of the three holds two distinct keys and the key
			// cannot be what refuses it.
			tableKey := func(table query.TableRef) string {
				return table.Definition().QualifiedName() + "\x00" + table.Alias()
			}
			require.NotEqual(t, tableKey(from), tableKey(joined),
				"the pair must hold distinct table keys, so the check cannot be riding on the key")

			fromID := from.Column("id")
			joinedID := joined.Column("id")

			join := query.InnerJoin(joined, query.Equal(fromID, joinedID))
			_, err := query.NewJoinedSelect(from, []query.Join{join}, nil, fromID)
			requireQueryValidationError(t, err)
			require.ErrorContains(t, err, testCase.message)
		})
	}

	accepted := map[string]struct {
		from   func(*testing.T) query.TableRef
		joined func(*testing.T) query.TableRef
	}{
		// Each source renders its columns under its own "schema"."table"
		// prefix, so nothing is ambiguous.
		"same name in two schemas, both unaliased": {
			from:   func(t *testing.T) query.TableRef { return query.MustTableRef(qualifiedUsers("tenant_a")) },
			joined: func(t *testing.T) query.TableRef { return query.MustTableRef(qualifiedUsers("tenant_b")) },
		},
		"same name in two schemas under distinct aliases": {
			from:   func(t *testing.T) query.TableRef { return aliasOf(t, qualifiedUsers("tenant_a"), "a") },
			joined: func(t *testing.T) query.TableRef { return aliasOf(t, qualifiedUsers("tenant_b"), "b") },
		},
		"one table joined to itself under a distinct alias": {
			from:   func(t *testing.T) query.TableRef { return query.MustTableRef(usersTable()) },
			joined: func(t *testing.T) query.TableRef { return aliasOf(t, usersTable(), "manager") },
		},
		"two differently named tables": {
			from:   func(t *testing.T) query.TableRef { return query.MustTableRef(usersTable()) },
			joined: func(t *testing.T) query.TableRef { return query.MustTableRef(ordersTable()) },
		},
	}
	for name, testCase := range accepted {
		t.Run(name, func(t *testing.T) {
			from := testCase.from(t)
			joined := testCase.joined(t)

			fromID := from.Column("id")
			joinedID := joined.Column("id")

			join := query.InnerJoin(joined, query.Equal(fromID, joinedID))
			_, err := query.NewJoinedSelect(from, []query.Join{join}, nil, fromID)
			require.NoError(t, err)
		})
	}
}

// TestSelectRejectsSharedNameAddedByWithJoin pins that the check runs on every
// path that adds a source, not only on the constructor.
func TestSelectRejectsSharedNameAddedByWithJoin(t *testing.T) {
	users := query.MustTableRef(usersTable())
	descriptor := usersTable()
	descriptor.Schema = "tenant_a"
	tenantUsers := query.MustTableRef(descriptor)

	usersID := users.Column("id")
	tenantID := tenantUsers.Column("id")

	statement, err := query.NewSelect(users, usersID)
	require.NoError(t, err)

	_, err = statement.WithJoin(query.InnerJoin(tenantUsers, query.Equal(usersID, tenantID)))
	require.ErrorContains(t, err, `table "tenant_a.users" is referred to as "users", which already refers to table "users"`)
}

var (
	_ query.Projection = query.ColumnRef{}
	_ query.Projection = query.Function{}
	_ query.Projection = query.ExpressionProjection{}
)

// TestBareColumnRefIsAProjection pins that a ColumnRef needs no query.Project
// wrapper: passed to NewSelect directly, it builds the same statement
// query.Project(column) built before ColumnRef satisfied Projection, and
// column.As sets the result alias the same way Project(column).As did.
func TestBareColumnRefIsAProjection(t *testing.T) {
	users := query.MustTableRef(usersTable())
	id := users.Column("id")
	email := users.Column("email")

	bare, err := query.NewSelect(users, id, email.As("user_email"))
	require.NoError(t, err)

	projections := bare.Projections()
	require.Len(t, projections, 2)
	require.Equal(t, "", projections[0].ResultAlias())
	require.Equal(t, query.Expression(id), projections[0].ProjectedExpression())
	require.Equal(t, "user_email", projections[1].ResultAlias())
	require.Equal(t, query.Expression(email), projections[1].ProjectedExpression())

	wrapped, err := query.NewSelect(users, query.Project(id), query.Project(email).As("user_email"))
	require.NoError(t, err)
	require.Equal(t, wrapped.Projections()[0].ProjectedExpression(), bare.Projections()[0].ProjectedExpression())
	require.Equal(t, wrapped.Projections()[1].ResultAlias(), bare.Projections()[1].ResultAlias())
}

// TestBareFunctionIsAProjection pins the same for a function call, which is
// what a SELECT list holds besides columns. An unaliased call reports an empty
// result alias, and As names it without the call growing an alias of its own:
// the alias lives in the ExpressionProjection As returns, so the receiver is
// unchanged and stays usable as an argument to another call.
func TestBareFunctionIsAProjection(t *testing.T) {
	users := query.MustTableRef(usersTable())
	email := users.Column("email")

	upper := query.Upper(email)
	lower := query.Lower(email)

	statement, err := query.NewSelect(users, upper, lower.As("lower_email"))
	require.NoError(t, err)
	require.NoError(t, statement.Validate())

	projections := statement.Projections()
	require.Len(t, projections, 2)
	require.Equal(t, "", projections[0].ResultAlias())
	require.Equal(t, query.Expression(upper), projections[0].ProjectedExpression())
	require.Equal(t, "lower_email", projections[1].ResultAlias())
	require.Equal(t, query.Expression(lower), projections[1].ProjectedExpression())

	require.Equal(t, "", lower.ResultAlias(), "As must not give the receiver an alias")

	wrapped, err := query.NewSelect(users, query.Project(upper), query.Project(lower).As("lower_email"))
	require.NoError(t, err)
	require.Equal(t, wrapped.Projections()[0].ProjectedExpression(), projections[0].ProjectedExpression())
	require.Equal(t, wrapped.Projections()[1].ResultAlias(), projections[1].ResultAlias())
}

// TestSelectRejectsNilProjection pins that a nil Projection element reports a
// validation error naming its position, rather than panicking when validation
// dereferences it.
func TestSelectRejectsNilProjection(t *testing.T) {
	users := query.MustTableRef(usersTable())

	_, err := query.NewSelect(users, nil)
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "projections[0]")
	require.ErrorContains(t, err, "must not be nil")
}

func requireQueryValidationError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var validationErr *query.ValidationError
	require.True(t, errors.As(err, &validationErr))
}

func usersTable() schema.TableDef {
	return schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	}
}

func ordersTable() schema.TableDef {
	return schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.FloatType{}},
		},
		PrimaryKey: []string{"id"},
	}
}
