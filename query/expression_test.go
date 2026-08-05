package query_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/query"
	"github.com/stretchr/testify/require"
)

func TestMembershipKeepsOperands(t *testing.T) {
	users, err := query.NewTable(usersTable())
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
