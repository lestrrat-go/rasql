package query_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/query"
	"github.com/stretchr/testify/require"
)

// TestOrderResultAliasResolvesAgainstAsProjection pins that AscResult/DescResult
// name the result an As alias gave a projection, which is the ordinary case the
// design exists for: a statement projects a computed column under a name and
// orders by the same projection value instead of repeating its expression or
// its alias as a second string.
func TestOrderResultAliasResolvesAgainstAsProjection(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	addr := users.Column("email").As("addr")

	statement, err := query.NewSelect(users, addr)
	require.NoError(t, err)
	statement, err = statement.WithOrder(query.AscResult(addr))
	require.NoError(t, err)
	require.NoError(t, statement.Validate())
}

// TestOrderResultAliasResolvesAgainstUnaliasedColumnName pins the other source
// of a result name: a ColumnRef selected without a wrapper is reported under
// its own column name, so an order term may name the same ColumnRef directly,
// with no As at all.
func TestOrderResultAliasResolvesAgainstUnaliasedColumnName(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	email := users.Column("email")

	statement, err := query.NewSelect(users, email)
	require.NoError(t, err)
	statement, err = statement.WithOrder(query.DescResult(email))
	require.NoError(t, err)
	require.NoError(t, statement.Validate())
}

// TestOrderResultAliasRefusesUnknownName pins decision 2's first refusal: a
// projection whose result name no projection of the statement reports is not
// silently accepted the way SQLite would resolve nothing and error at the
// server; rasql refuses it in Go with a message telling the caller how to fix
// it, which here is that the caller built a projection and forgot to add it
// to Project.
func TestOrderResultAliasRefusesUnknownName(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	email := users.Column("email")

	statement, err := query.NewSelect(users, email)
	require.NoError(t, err)
	_, err = statement.WithOrder(query.AscResult(email.As("nope")))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "order_by[0]")
	require.ErrorContains(t, err, `"nope"`)
	require.ErrorContains(t, err, "no projection")
}

// TestOrderResultAliasRefusesTwoExplicitAliasesSharingAName pins decision 2's
// ambiguity refusal for the shape PostgreSQL and MySQL both call ambiguous:
// two projections given the same alias explicitly. AscResult names one of the
// two projection values directly; the ambiguity is still judged by the name
// they share, not by which of the two values was passed to it.
func TestOrderResultAliasRefusesTwoExplicitAliasesSharingAName(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	x1 := users.Column("id").As("x")
	x2 := users.Column("email").As("x")

	statement, err := query.NewSelect(users, x1, x2)
	require.NoError(t, err)
	_, err = statement.WithOrder(query.AscResult(x1))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "order_by[0]")
	require.ErrorContains(t, err, `"x"`)
	require.ErrorContains(t, err, "ambiguous")
}

// TestOrderResultAliasRefusesAliasCollidingWithUnaliasedColumn pins the other
// ambiguous shape the matrix records: an explicit alias that collides with an
// unaliased column's own name is refused exactly like two explicit aliases
// colliding, because the engines' rule is about duplicate result names, not
// about which projection carries an As.
func TestOrderResultAliasRefusesAliasCollidingWithUnaliasedColumn(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	id := users.Column("id")
	aliasedEmail := users.Column("email").As("id")

	statement, err := query.NewSelect(users, id, aliasedEmail)
	require.NoError(t, err)
	_, err = statement.WithOrder(query.AscResult(aliasedEmail))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "order_by[0]")
	require.ErrorContains(t, err, "ambiguous")
}

// TestOrderResultAliasRefusesIllegalIdentifier pins that AscResult/DescResult
// runs the same identifier check a projection's own As does, reusing
// validateAlias against the name the passed projection reports rather than
// accepting anything As was given.
func TestOrderResultAliasRefusesIllegalIdentifier(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	addr := users.Column("email").As("addr")

	statement, err := query.NewSelect(users, addr)
	require.NoError(t, err)
	_, err = statement.WithOrder(query.AscResult(users.Column("email").As("not a valid identifier")))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "order_by[0]")
	require.ErrorContains(t, err, "invalid alias")
}

// TestOrderResultAliasRefusesProjectionWithNoResultName pins decision 2's other
// refusal: a projection reported under whatever name the database picks for
// it, such as an unaliased aggregate call, has no portable name to order by.
// rasql refuses it in Go, rather than rendering an ORDER BY term naming
// nothing the statement's own SELECT list can be trusted to agree with across
// dialects.
func TestOrderResultAliasRefusesProjectionWithNoResultName(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)

	statement, err := query.NewSelect(users, query.CountAll().As("n"))
	require.NoError(t, err)
	_, err = statement.WithOrder(query.AscResult(query.CountAll()))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "order_by[0]")
	require.ErrorContains(t, err, "no result name")
	require.ErrorContains(t, err, "As")
}

// TestOrderReportsWhichKindItCarries pins Expression and ResultProjection as
// the pair that tell an Asc/Desc term from an AscResult/DescResult term:
// exactly one of them ever reports something for a given Order value.
func TestOrderReportsWhichKindItCarries(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	email := users.Column("email")
	addr := email.As("addr")

	byExpression := query.Asc(email)
	require.Equal(t, query.Expression(email), byExpression.Expression())
	projection, ok := byExpression.ResultProjection()
	require.False(t, ok)
	require.Nil(t, projection)

	byResult := query.AscResult(addr)
	require.Nil(t, byResult.Expression())
	projection, ok = byResult.ResultProjection()
	require.True(t, ok)
	require.Equal(t, addr, projection)
}

// TestOrderResultAliasAcceptedOverImplicitGroupAggregate pins that an alias
// order term skips validateOrder's aggregate reasoning entirely: it names a
// projection validateProjectionSet already judged, so an alias naming an
// aggregate projection over one implicit group needs no aggregate rule of its
// own, unlike Asc(query.CountAll()) which does have to satisfy that rule.
func TestOrderResultAliasAcceptedOverImplicitGroupAggregate(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	n := query.CountAll().As("n")

	statement, err := query.NewSelect(users, n)
	require.NoError(t, err)
	statement, err = statement.WithOrder(query.DescResult(n))
	require.NoError(t, err)
	require.NoError(t, statement.Validate())
}

// TestOrderResultAliasAcceptedOverExplicitGroupBy pins the same skip for a
// statement that groups explicitly, with a plain column beside the aggregate.
func TestOrderResultAliasAcceptedOverExplicitGroupBy(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	email := users.Column("email")
	n := query.CountAll().As("n")

	statement, err := query.NewGroupedSelect(users, []query.Expression{email}, email, n)
	require.NoError(t, err)
	statement, err = statement.WithOrder(query.DescResult(n))
	require.NoError(t, err)
	require.NoError(t, statement.Validate())
}

// TestOrderResultAliasResolvesWithinScalarSubquery pins that an alias order
// term inside a query.Scalar subquery resolves against that subquery's own
// projections, not the outer statement's: Select.Validate calls the
// subquery's own Validate, which computes resultNames from the subquery's own
// projection list.
func TestOrderResultAliasResolvesWithinScalarSubquery(t *testing.T) {
	orders, err := query.NewTableRef(ordersTable())
	require.NoError(t, err)
	total := orders.Column("amount").As("total")

	subquery, err := query.NewSelect(orders, total)
	require.NoError(t, err)
	subquery, err = subquery.WithOrder(query.AscResult(total))
	require.NoError(t, err)
	subquery, err = subquery.WithLimit(1)
	require.NoError(t, err)

	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	id := users.Column("id")

	statement, err := query.NewSelect(users, id)
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.GreaterThan(id, query.Scalar(subquery)))
	require.NoError(t, err)
	require.NoError(t, statement.Validate())
}
