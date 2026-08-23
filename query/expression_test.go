package query_test

import (
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/query"
	"github.com/stretchr/testify/require"
)

// TestColumnRefValidate pins ColumnRef.Validate for a valid column and for
// the zero ColumnRef, whose nil source is reported rather than collapsed into
// the missing-column message. The missing-column cases, including the
// qualified and aliased table forms, live in expression_internal_test.go:
// TableRef.Column still zeroes its result on a bad name until PR 3 lands, so
// no exported constructor can build a ColumnRef pairing a valid source with a
// name it does not hold.
func TestColumnRefValidate(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	require.NoError(t, id.Validate())

	var zero query.ColumnRef
	require.True(t, errors.Is(zero.Validate(), query.ErrNilTable))
}

func TestMembershipKeepsOperands(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)

	values := []query.Expression{query.Bind(1), query.Bind(2)}
	in := query.In(id, values...)
	require.Equal(t, query.Expression(id), in.Expression())
	require.Equal(t, values, in.Values())
	require.False(t, in.Not())

	notIn := query.NotIn(id, values...)
	require.Equal(t, query.Expression(id), notIn.Expression())
	require.Equal(t, values, notIn.Values())
	require.True(t, notIn.Not())

	// Mutating the slice returned by Values does not change the expression.
	returned := in.Values()
	returned[0] = query.Bind(99)
	require.Equal(t, values, in.Values())

	// Mutating the variadic slice passed in does not change the expression either.
	values[0] = query.Bind(99)
	require.Equal(t, []query.Expression{query.Bind(1), query.Bind(2)}, in.Values())
}

func TestSubqueryKeepsItsStatement(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	userID, err := users.Column("id")
	require.NoError(t, err)
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
	userID, err := users.Column("id")
	require.NoError(t, err)
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
