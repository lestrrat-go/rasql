package render_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestSelectBuilderBuildsRenderedStatement(t *testing.T) {
	users := fluentUsers(t)

	rendered, err := render.SelectFrom(dialect.PostgreSQL(), users).
		Select("id", "email").
		WhereEqual("id", 42).
		OrderDesc("email").
		Limit(20).
		Offset(10).
		Build()
	require.NoError(t, err)

	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)
	statement, err := query.NewSelect(users, query.Project(id), query.Project(email))
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.Equal(id, query.Bind(42)))
	require.NoError(t, err)
	statement, err = statement.WithOrder(query.Desc(email))
	require.NoError(t, err)
	statement, err = statement.WithLimit(20)
	require.NoError(t, err)
	statement, err = statement.WithOffset(10)
	require.NoError(t, err)
	expected, err := render.Select(dialect.PostgreSQL(), statement)
	require.NoError(t, err)

	require.Equal(t, expected.SQL(), rendered.SQL())
	require.Equal(t, expected.Args(), rendered.Args())
}

func TestSelectBuilderIsImmutable(t *testing.T) {
	users := fluentUsers(t)

	base := render.SelectFrom(dialect.PostgreSQL(), users).Select("id")
	filtered := base.WhereEqual("id", 42)

	baseStatement, err := base.Build()
	require.NoError(t, err)
	require.NotContains(t, baseStatement.SQL(), " WHERE ")
	filteredStatement, err := filtered.Build()
	require.NoError(t, err)
	require.Contains(t, filteredStatement.SQL(), " WHERE ")
}

func TestSelectBuilderReportsBuildErrors(t *testing.T) {
	users := fluentUsers(t)

	_, err := render.SelectFrom(dialect.PostgreSQL(), users).Select("missing").Build()
	require.Error(t, err)
	_, err = render.SelectFrom(dialect.PostgreSQL(), users).Select("id").Limit(-1).Build()
	require.Error(t, err)
	_, err = render.SelectFrom(dialect.PostgreSQL(), users).Select("id").Where(nil).Build()
	require.Error(t, err)
	_, err = render.SelectFrom(nil, users).Select("id").Build()
	require.Error(t, err)
}

func TestSelectBuilderCombinesPredicates(t *testing.T) {
	users := fluentUsers(t)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)

	t.Run("two Where calls combine with AND", func(t *testing.T) {
		statement, err := render.SelectFrom(dialect.PostgreSQL(), users).
			Select("id").
			WhereEqual("id", 42).
			Where(query.Like(email, query.Bind("%@example.com"))).
			Build()
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id" FROM "users" WHERE (("users"."id" = $1) AND ("users"."email" LIKE $2))`,
			statement.SQL())
		require.Equal(t, []any{42, "%@example.com"}, statement.Args())
	})

	t.Run("Where and WhereEqual combine in both orders", func(t *testing.T) {
		whereFirst, err := render.SelectFrom(dialect.PostgreSQL(), users).
			Select("id").
			Where(query.Like(email, query.Bind("%@example.com"))).
			WhereEqual("id", 42).
			Build()
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id" FROM "users" WHERE (("users"."email" LIKE $1) AND ("users"."id" = $2))`,
			whereFirst.SQL())

		whereEqualFirst, err := render.SelectFrom(dialect.PostgreSQL(), users).
			Select("id").
			WhereEqual("id", 42).
			Where(query.Like(email, query.Bind("%@example.com"))).
			Build()
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id" FROM "users" WHERE (("users"."id" = $1) AND ("users"."email" LIKE $2))`,
			whereEqualFirst.SQL())
	})

	t.Run("three predicates render one flat AND", func(t *testing.T) {
		statement, err := render.SelectFrom(dialect.PostgreSQL(), users).
			Select("id").
			WhereEqual("id", 42).
			Where(query.Like(email, query.Bind("%@example.com"))).
			Where(query.IsNotNull(email)).
			Build()
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id" FROM "users" WHERE (("users"."id" = $1) AND ("users"."email" LIKE $2) AND ("users"."email" IS NOT NULL))`,
			statement.SQL())
		require.NotContains(t, statement.SQL(), `") AND ("users"."email" LIKE $2)) AND`)
	})

	t.Run("a lone Or is not wrapped", func(t *testing.T) {
		statement, err := render.SelectFrom(dialect.PostgreSQL(), users).
			Select("id").
			Where(query.Or(
				query.Equal(email, query.Bind("ada@example.com")),
				query.Equal(email, query.Bind("bob@example.com")),
			)).
			Build()
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id" FROM "users" WHERE (("users"."email" = $1) OR ("users"."email" = $2))`,
			statement.SQL())
	})

	t.Run("Or then Where nests correctly", func(t *testing.T) {
		statement, err := render.SelectFrom(dialect.PostgreSQL(), users).
			Select("id").
			Where(query.Or(
				query.Equal(email, query.Bind("ada@example.com")),
				query.Equal(email, query.Bind("bob@example.com")),
			)).
			Where(query.GreaterThan(id, query.Bind(10))).
			Build()
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id" FROM "users" WHERE ((("users"."email" = $1) OR ("users"."email" = $2)) AND ("users"."id" > $3))`,
			statement.SQL())
		require.Equal(t, []any{"ada@example.com", "bob@example.com", 10}, statement.Args())
	})

	t.Run("no predicate renders no WHERE", func(t *testing.T) {
		statement, err := render.SelectFrom(dialect.PostgreSQL(), users).Select("id").Build()
		require.NoError(t, err)
		require.NotContains(t, statement.SQL(), " WHERE ")
	})

	t.Run("nil second call errors without dropping the first", func(t *testing.T) {
		_, err := render.SelectFrom(dialect.PostgreSQL(), users).
			Select("id").
			WhereEqual("id", 42).
			Where(nil).
			Build()
		require.Error(t, err)
	})
}

func TestSelectBuilderPredicatesDoNotAlias(t *testing.T) {
	users := fluentUsers(t)
	email, err := users.Column("email")
	require.NoError(t, err)

	// Three predicates leave the accumulated slice with spare backing-array
	// capacity (Go grows a nil slice 0 -> 1 -> 2 -> 4), which is what exposes
	// the aliasing hazard clone() must guard against.
	base := render.SelectFrom(dialect.PostgreSQL(), users).
		Select("id").
		WhereEqual("id", 1).
		WhereEqual("id", 2).
		WhereEqual("id", 3)

	first := base.Where(query.Equal(email, query.Bind("ada@example.com")))
	second := base.Where(query.Equal(email, query.Bind("bob@example.com")))

	firstStatement, err := first.Build()
	require.NoError(t, err)
	secondStatement, err := second.Build()
	require.NoError(t, err)

	require.Contains(t, firstStatement.Args(), "ada@example.com")
	require.NotContains(t, firstStatement.Args(), "bob@example.com")

	require.Contains(t, secondStatement.Args(), "bob@example.com")
	require.NotContains(t, secondStatement.Args(), "ada@example.com")
}

func fluentUsers(t *testing.T) query.Table {
	t.Helper()
	users, err := query.NewTable(schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	return users
}
