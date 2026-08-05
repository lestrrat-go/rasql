package rasql_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/query"
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
}
