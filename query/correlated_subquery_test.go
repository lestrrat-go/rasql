package query_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/query"
	"github.com/stretchr/testify/require"
)

// correlatedFixture is the pair of tables every test below builds against: a
// users statement enclosing an orders subquery, which is the shape a correlated
// EXISTS and a correlated scalar subquery both take.
type correlatedFixture struct {
	users       query.TableRef
	usersID     query.ColumnRef
	orders      query.TableRef
	ordersID    query.ColumnRef
	ordersUser  query.ColumnRef
	ordersTotal query.ColumnRef
}

func newCorrelatedFixture(t *testing.T) correlatedFixture {
	t.Helper()

	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	orders, err := query.NewTableRef(ordersTable())
	require.NoError(t, err)
	return correlatedFixture{
		users:       users,
		usersID:     users.Column("id"),
		orders:      orders,
		ordersID:    orders.Column("id"),
		ordersUser:  orders.Column("user_id"),
		ordersTotal: orders.Column("amount"),
	}
}

// ordersOfUser is the correlated subquery the tests reuse: every order of the
// user the enclosing statement is on, projecting whatever projections says.
func (f correlatedFixture) ordersOfUser(t *testing.T, projections ...query.Projection) query.Select {
	t.Helper()

	statement, err := query.NewSelect(f.orders, projections...)
	require.NoError(t, err)
	statement, err = statement.WithCorrelation(f.users)
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.Equal(f.ordersUser, f.usersID))
	require.NoError(t, err)
	return statement
}

// TestExistsAcceptsAnyProjectionCount pins the one rule EXISTS does not share
// with Scalar, InSelect and NotInSelect. Those three put the subquery where SQL
// expects a value, so exactly one projection is legal; EXISTS reads no value at
// all, so any number is, and SELECT 1 -- Project(Bind(1)) here -- is the
// conventional body.
func TestExistsAcceptsAnyProjectionCount(t *testing.T) {
	f := newCorrelatedFixture(t)

	for name, projections := range map[string][]query.Projection{
		"select 1":        {query.Project(query.Bind(1))},
		"two projections": {f.ordersID, f.ordersTotal},
	} {
		t.Run(name, func(t *testing.T) {
			subquery := f.ordersOfUser(t, projections...)
			statement, err := query.NewSelect(f.users, f.usersID)
			require.NoError(t, err)
			statement, err = statement.WithWhere(query.Exists(subquery))
			require.NoError(t, err)
			require.NoError(t, statement.Validate())

			statement, err = statement.WithWhere(query.NotExists(subquery))
			require.NoError(t, err)
			require.NoError(t, statement.Validate())
		})
	}

	// The same two-projection statement is refused where a value is expected,
	// so the relaxation above reaches EXISTS alone.
	twoProjections := f.ordersOfUser(t, f.ordersID, f.ordersTotal)
	statement, err := query.NewSelect(f.users, f.usersID)
	require.NoError(t, err)
	_, err = statement.WithWhere(query.GreaterThan(f.usersID, query.Scalar(twoProjections)))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "must select exactly one expression, got 2")
}

// TestCorrelatedSubqueryReadsTheEnclosingTable covers the placements a
// correlated subquery reaches: a predicate, through EXISTS, and a projection,
// through Scalar. Both are the same mechanism, so a scalar subquery counting one
// user's orders beside that user has to work exactly as the EXISTS does.
func TestCorrelatedSubqueryReadsTheEnclosingTable(t *testing.T) {
	f := newCorrelatedFixture(t)

	t.Run("exists in a predicate", func(t *testing.T) {
		statement, err := query.NewSelect(f.users, f.usersID)
		require.NoError(t, err)
		statement, err = statement.WithWhere(query.Exists(f.ordersOfUser(t, query.Project(query.Bind(1)))))
		require.NoError(t, err)
		require.NoError(t, statement.Validate())
	})

	t.Run("scalar in a projection", func(t *testing.T) {
		counted := f.ordersOfUser(t, query.Project(query.CountAll()))
		statement, err := query.NewSelect(f.users, f.usersID, query.Project(query.Scalar(counted)).As("orders"))
		require.NoError(t, err)
		require.NoError(t, statement.Validate())
	})

	t.Run("three levels deep", func(t *testing.T) {
		// The innermost statement correlates with the outermost table, two
		// levels above it, so the scope a subquery inherits has to accumulate
		// rather than name only the statement directly enclosing it.
		items, err := f.orders.As("items")
		require.NoError(t, err)
		innermost, err := query.NewSelect(items, query.Project(query.Bind(1)))
		require.NoError(t, err)
		innermost, err = innermost.WithCorrelation(f.users)
		require.NoError(t, err)
		innermost, err = innermost.WithWhere(query.Equal(items.Column("user_id"), f.usersID))
		require.NoError(t, err)

		// The middle statement declares users as well, even though it names no
		// column of it: without that, validating the middle statement on its
		// own has nothing saying a third statement is coming, and refuses the
		// declaration the innermost one makes.
		middle, err := query.NewSelect(f.orders, query.Project(query.Bind(1)))
		require.NoError(t, err)
		middle, err = middle.WithCorrelation(f.users)
		require.NoError(t, err)
		middle, err = middle.WithWhere(query.Exists(innermost))
		require.NoError(t, err)

		statement, err := query.NewSelect(f.users, f.usersID)
		require.NoError(t, err)
		statement, err = statement.WithWhere(query.Exists(middle))
		require.NoError(t, err)
		require.NoError(t, statement.Validate())
	})
}

// TestCorrelationMustBeDeclared pins that a subquery reads an enclosing table
// only when it says so. The declaration is what lets a statement be validated
// while it is still being built, before any statement encloses it.
func TestCorrelationMustBeDeclared(t *testing.T) {
	f := newCorrelatedFixture(t)

	subquery, err := query.NewSelect(f.orders, query.Project(query.Bind(1)))
	require.NoError(t, err)
	_, err = subquery.WithWhere(query.Equal(f.ordersUser, f.usersID))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, `references table "users" outside the statement`)
}

// TestCorrelationMustNameAnEnclosingTable pins the other end of the check. A
// declaration is verified against the statement that really encloses the
// subquery, so a subquery attached to the wrong statement is reported rather
// than rendering SQL that names a table the FROM clause never lists.
func TestCorrelationMustNameAnEnclosingTable(t *testing.T) {
	f := newCorrelatedFixture(t)

	subquery := f.ordersOfUser(t, query.Project(query.Bind(1)))
	// The enclosing statement selects from orders, not users, so the
	// correlation the subquery declared has nothing to resolve against.
	elsewhere, err := f.orders.As("elsewhere")
	require.NoError(t, err)
	statement, err := query.NewSelect(elsewhere, elsewhere.Column("id"))
	require.NoError(t, err)
	_, err = statement.WithWhere(query.Exists(subquery))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, `declares a correlation with table "users", which the enclosing statement does not carry`)
}

// TestCorrelatedSubqueryRefusesAnAmbiguousSource pins the resolution rule. A
// subquery that declared a correlation with a table it also selects from can
// reach both copies, and SQL answers every reference to them from the subquery's
// own, silently: the enclosing row the declaration asked for is then unreadable
// with nothing in the Go code saying so. rasql refuses the pair and names the
// alias that separates them, which is what it already does for two tables of one
// statement a server cannot tell apart.
//
// A subquery that declared nothing reaches only its own tables, so selecting
// from a table the enclosing statement also selects from is not ambiguous and
// stays legal. That is the shape a DELETE's IN (SELECT … FROM the target …)
// takes, which render refuses for MySQL alone.
func TestCorrelatedSubqueryRefusesAnAmbiguousSource(t *testing.T) {
	f := newCorrelatedFixture(t)

	t.Run("subquery selects from a table it correlates with", func(t *testing.T) {
		subquery, err := query.NewSelect(f.users, query.Project(query.Bind(1)))
		require.NoError(t, err)
		_, err = subquery.WithCorrelation(f.users)
		requireQueryValidationError(t, err)
		require.ErrorContains(t, err, "give one of them a distinct alias")
	})

	t.Run("subquery joins a table it correlates with", func(t *testing.T) {
		subquery, err := query.NewSelect(f.orders, query.Project(query.Bind(1)))
		require.NoError(t, err)
		subquery, err = subquery.WithCorrelation(f.users)
		require.NoError(t, err)
		_, err = subquery.WithJoin(query.InnerJoin(f.users, query.Equal(f.ordersUser, f.usersID)))
		requireQueryValidationError(t, err)
		require.ErrorContains(t, err, "give one of them a distinct alias")
	})

	t.Run("selecting from the enclosing table without declaring it stays legal", func(t *testing.T) {
		subquery, err := query.NewSelect(f.users, f.usersID)
		require.NoError(t, err)
		statement, err := query.NewSelect(f.users, f.usersID)
		require.NoError(t, err)
		statement, err = statement.WithWhere(query.InSelect(f.usersID, subquery))
		require.NoError(t, err)
		require.NoError(t, statement.Validate())
	})

	t.Run("an alias repairs it", func(t *testing.T) {
		// The same shape as the first case, with the subquery's own users
		// aliased, so each scope answers to a leading identifier of its own.
		others, err := f.users.As("others")
		require.NoError(t, err)
		subquery, err := query.NewSelect(others, query.Project(query.Bind(1)))
		require.NoError(t, err)
		subquery, err = subquery.WithCorrelation(f.users)
		require.NoError(t, err)
		subquery, err = subquery.WithWhere(query.NotEqual(others.Column("id"), f.usersID))
		require.NoError(t, err)
		statement, err := query.NewSelect(f.users, f.usersID)
		require.NoError(t, err)
		statement, err = statement.WithWhere(query.Exists(subquery))
		require.NoError(t, err)
		require.NoError(t, statement.Validate())
	})
}

// TestSubqueryKeepsAggregateRulesInItsOwnScope pins that the aggregate placement
// rules stop at the scope boundary in both directions. An aggregate inside a
// subquery belongs to that subquery's clause and its own grouping, and a column
// the subquery reads belongs to the subquery's rows, so neither reaches the
// enclosing statement's projection set.
func TestSubqueryKeepsAggregateRulesInItsOwnScope(t *testing.T) {
	f := newCorrelatedFixture(t)

	t.Run("an inner aggregate does not aggregate the outer statement", func(t *testing.T) {
		// SELECT users.id, (SELECT AVG(orders.amount) FROM orders WHERE …)
		// reads a bare column beside a subquery that aggregates. Reporting the
		// inner AVG outward would make this look like an ungrouped statement
		// mixing an aggregate with a bare column, which validation refuses.
		average := f.ordersOfUser(t, query.Project(query.Avg(f.ordersTotal)))
		statement, err := query.NewSelect(f.users, f.usersID, query.Project(query.Scalar(average)).As("average"))
		require.NoError(t, err)
		require.NoError(t, statement.Validate())
	})

	t.Run("an inner column does not make the outer statement read one", func(t *testing.T) {
		// COUNT(*) beside an EXISTS whose subquery reads columns: the columns
		// belong to the subquery's rows, so the enclosing statement still
		// aggregates over one implicit group and needs no GROUP BY.
		statement, err := query.NewSelect(f.users, query.Project(query.CountAll()).As("total"))
		require.NoError(t, err)
		statement, err = statement.WithWhere(query.Exists(f.ordersOfUser(t, f.ordersID)))
		require.NoError(t, err)
		require.NoError(t, statement.Validate())
	})

	t.Run("an aggregate is legal inside a subquery inside an aggregate", func(t *testing.T) {
		// SUM((SELECT AVG(…) …)) nests one aggregate inside another only in
		// Go: the subquery is its own statement, so the SQL never nests the
		// two calls and the depth an aggregate is judged at starts again
		// inside it.
		average := f.ordersOfUser(t, query.Project(query.Avg(f.ordersTotal)))
		statement, err := query.NewSelect(f.users, query.Project(query.Sum(query.Scalar(average))).As("total"))
		require.NoError(t, err)
		require.NoError(t, statement.Validate())
	})
}

// TestExistsFollowsTheSubqueryClauseRule pins that EXISTS reaches exactly the
// clauses every other subquery form does, and no others. It is a subquery like
// any other, so a DELETE's WHERE clause takes one and a RETURNING projection
// refuses one.
func TestExistsFollowsTheSubqueryClauseRule(t *testing.T) {
	f := newCorrelatedFixture(t)

	subquery, err := query.NewSelect(f.orders, query.Project(query.Bind(1)))
	require.NoError(t, err)
	statement, err := query.NewDelete(f.users)
	require.NoError(t, err)
	withWhere, err := statement.WithWhere(query.Exists(subquery))
	require.NoError(t, err)
	require.NoError(t, withWhere.Validate())

	_, err = statement.WithReturning(query.Project(query.Exists(subquery)).As("any_order"))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "runs a subquery in a RETURNING projection")
}

// TestWriteStatementsAcceptACorrelatedSubquery covers the three write clauses a
// subquery reaches: a DELETE's WHERE, an UPDATE's WHERE, and an UPDATE's SET
// assignment value. The row a correlated subquery reads is the row being
// written, so each of the three carries the write target as the table a
// correlation may name.
// TestCorrelatedWriteAgainstLiveDatabases in the root package runs all three
// against PostgreSQL, MySQL, and SQLite.
func TestWriteStatementsAcceptACorrelatedSubquery(t *testing.T) {
	f := newCorrelatedFixture(t)

	exists := query.Exists(f.ordersOfUser(t, f.ordersID))
	counted := query.Scalar(f.ordersOfUser(t, query.Project(query.CountAll())))

	deleteStatement, err := query.NewDelete(f.users)
	require.NoError(t, err)
	deleteWhere, err := deleteStatement.WithWhere(exists)
	require.NoError(t, err)
	require.NoError(t, deleteWhere.Validate())

	updateStatement, err := query.NewUpdate(f.users, query.Set(f.users.Column("email"), "ada@example.com"))
	require.NoError(t, err)
	updateWhere, err := updateStatement.WithWhere(exists)
	require.NoError(t, err)
	require.NoError(t, updateWhere.Validate())

	updateSet, err := query.NewUpdate(f.users, query.Set(f.users.Column("id"), counted))
	require.NoError(t, err)
	require.NoError(t, updateSet.Validate())
}

// TestWriteSubqueryStillNamesTheWriteTargetAsItsOwnSource pins what admitting a
// correlation into a write statement did not change. A subquery selecting from
// the write target declares no correlation, so it reads only its own copy of
// that table and stays legal here, exactly as it was before correlation existed;
// render is what refuses it for MySQL, whose error 1093 rejects the shape.
// Declaring a correlation with that same target is the ambiguous pair instead,
// and validation refuses it.
func TestWriteSubqueryStillNamesTheWriteTargetAsItsOwnSource(t *testing.T) {
	f := newCorrelatedFixture(t)

	deleteStatement, err := query.NewDelete(f.users)
	require.NoError(t, err)

	overTarget, err := query.NewSelect(f.users, f.usersID)
	require.NoError(t, err)
	sameTable, err := deleteStatement.WithWhere(query.InSelect(f.usersID, overTarget))
	require.NoError(t, err)
	require.NoError(t, sameTable.Validate())

	_, err = overTarget.WithCorrelation(f.users)
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "give one of them a distinct alias")
}

// TestWriteCorrelationMustNameTheWriteTarget pins the declaration check at the
// write end. A subquery correlating with a table the write statement does not
// target has nothing to resolve against, so it is refused rather than rendered
// against a table the statement never names.
func TestWriteCorrelationMustNameTheWriteTarget(t *testing.T) {
	f := newCorrelatedFixture(t)

	subquery := f.ordersOfUser(t, f.ordersID)
	deleteStatement, err := query.NewDelete(f.orders)
	require.NoError(t, err)
	_, err = deleteStatement.WithWhere(query.Exists(subquery))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, `declares a correlation with table "users", which the enclosing statement does not carry`)
}
