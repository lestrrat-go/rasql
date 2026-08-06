package rasql_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestSelectBuilder(t *testing.T) {
	t.Run("repeated WhereEqual combines with AND", func(t *testing.T) {
		users := deleteUsersTable(t)
		client := clientForBuild(t)
		statement, err := client.SelectFrom(users.QueryTable()).
			Select("id", "email").
			WhereEqual("id", 42).
			WhereEqual("email", "ada@example.com").
			Build()
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id", "users"."email" FROM "users" WHERE (("users"."id" = $1) AND ("users"."email" = $2))`,
			statement.SQL())
		require.Equal(t, []any{42, "ada@example.com"}, statement.Args())
	})

	t.Run("Where after WhereEqual combines with AND", func(t *testing.T) {
		users := deleteUsersTable(t)
		client := clientForBuild(t)
		email, err := users.Column("email")
		require.NoError(t, err)
		statement, err := client.SelectFrom(users.QueryTable()).
			Select("id", "email").
			WhereEqual("id", 42).
			Where(query.Equal(email, query.Bind("ada@example.com"))).
			Build()
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id", "users"."email" FROM "users" WHERE (("users"."id" = $1) AND ("users"."email" = $2))`,
			statement.SQL())
		require.Equal(t, []any{42, "ada@example.com"}, statement.Args())
	})

	t.Run("a lone Where is unchanged", func(t *testing.T) {
		users := deleteUsersTable(t)
		client := clientForBuild(t)
		statement, err := client.SelectFrom(users.QueryTable()).
			Select("id", "email").
			WhereEqual("id", 42).
			Build()
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id", "users"."email" FROM "users" WHERE ("users"."id" = $1)`,
			statement.SQL())
		require.Equal(t, []any{42}, statement.Args())
	})

	// The grouping is validated together with the joins, so a joined table's
	// column may be grouped by. Attaching the joins after the first validation
	// refused it.
	t.Run("GroupBy accepts a joined table's column", func(t *testing.T) {
		users := deleteUsersTable(t)
		orders := selectOrdersTable(t)
		id, err := users.QueryTable().Column("id")
		require.NoError(t, err)
		orderUserID, err := orders.Column("user_id")
		require.NoError(t, err)

		statement, err := clientForBuild(t).SelectFrom(users.QueryTable()).
			Project(query.Project(orderUserID), query.Project(query.CountAll()).As("total")).
			Join(query.InnerJoin(orders, query.Equal(id, orderUserID))).
			GroupBy(orderUserID).
			Build()
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "orders"."user_id", COUNT(*) AS "total" FROM "users" INNER JOIN "orders" ON ("users"."id" = "orders"."user_id") GROUP BY "orders"."user_id"`,
			statement.SQL())
	})
}

// selectOrdersTable returns a table to join deleteUsersTable against.
func selectOrdersTable(t *testing.T) query.Table {
	t.Helper()

	orders, err := query.NewTable(schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	return orders
}
