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
	statement, err := query.NewSelect(users, id, email)
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

func TestSelectBuilderWhereInRendersOnePlaceholderPerValue(t *testing.T) {
	users := fluentUsers(t)

	rendered, err := render.SelectFrom(dialect.PostgreSQL(), users).
		Select("id", "email").
		WhereIn("id", 1, 2, 3).
		Build()
	require.NoError(t, err)

	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)
	statement, err := query.NewSelect(users, id, email)
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.In(id, query.Bind(1), query.Bind(2), query.Bind(3)))
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

func TestSelectBuilderWithDialectSetsTheRenderingDialect(t *testing.T) {
	users := fluentUsers(t)

	builder := render.SelectFrom(nil, users).Select("id")
	_, err := builder.Build()
	require.ErrorContains(t, err, "dialect must not be nil")

	rendered, err := builder.WithDialect(dialect.PostgreSQL()).Build()
	require.NoError(t, err)
	require.Equal(t, `SELECT "users"."id" FROM "users"`, rendered.SQL())
}

func TestSelectBuilderWithDialectDoesNotMutateReceiver(t *testing.T) {
	users := fluentUsers(t)

	base := render.SelectFrom(dialect.PostgreSQL(), users).Select("id")
	_ = base.WithDialect(dialect.MySQL())

	rendered, err := base.Build()
	require.NoError(t, err)
	require.Equal(t, `SELECT "users"."id" FROM "users"`, rendered.SQL())
}

func TestSelectBuilderBuildsCountStatement(t *testing.T) {
	users := fluentUsers(t)
	orders, err := query.NewTableRef(schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	orderUserID, err := orders.Column("user_id")
	require.NoError(t, err)

	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     "SELECT COUNT(*) AS \"count\" FROM \"users\" INNER JOIN \"orders\" ON (\"users\".\"id\" = \"orders\".\"user_id\") WHERE (\"users\".\"id\" = $1)",
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "SELECT COUNT(*) AS `count` FROM `users` INNER JOIN `orders` ON (`users`.`id` = `orders`.`user_id`) WHERE (`users`.`id` = ?)",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     "SELECT COUNT(*) AS \"count\" FROM \"users\" INNER JOIN \"orders\" ON (\"users\".\"id\" = \"orders\".\"user_id\") WHERE (\"users\".\"id\" = ?)",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.SelectFrom(test.dialect, users).
				Select("id", "email").
				Join(query.InnerJoin(orders, query.Equal(id, orderUserID))).
				WhereEqual("id", 42).
				OrderDesc("email").
				BuildCount()
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Equal(t, []any{42}, rendered.Args())
			require.NotContains(t, rendered.SQL(), " ORDER BY ")
		})
	}
}

func TestSelectBuilderBuildsDistinctStatement(t *testing.T) {
	users := fluentUsers(t)

	rendered, err := render.SelectFrom(dialect.PostgreSQL(), users).
		Select("email").
		Distinct().
		OrderAsc("email").
		Build()
	require.NoError(t, err)
	require.Equal(t, `SELECT DISTINCT "users"."email" FROM "users" ORDER BY "users"."email"`, rendered.SQL())
	require.Empty(t, rendered.Args())
}

func TestSelectBuilderDistinctIsImmutable(t *testing.T) {
	users := fluentUsers(t)

	base := render.SelectFrom(dialect.PostgreSQL(), users).Select("email")
	distinct := base.Distinct()

	baseStatement, err := base.Build()
	require.NoError(t, err)
	require.NotContains(t, baseStatement.SQL(), "DISTINCT")
	distinctStatement, err := distinct.Build()
	require.NoError(t, err)
	require.Contains(t, distinctStatement.SQL(), "SELECT DISTINCT ")
}

// TestSelectBuilderRejectsCountWithDistinct proves BuildCount refuses a
// distinct builder rather than rendering SELECT DISTINCT COUNT(*), which
// would always answer 1 instead of the number of distinct rows.
func TestSelectBuilderRejectsCountWithDistinct(t *testing.T) {
	users := fluentUsers(t)

	_, err := render.SelectFrom(dialect.PostgreSQL(), users).Select("email").Distinct().BuildCount()
	require.Error(t, err)
	require.ErrorContains(t, err, "cannot count a distinct statement")
}

func TestSelectBuilderBuildsGroupedStatement(t *testing.T) {
	users := fluentUsers(t)
	id, err := users.Column("id")
	require.NoError(t, err)

	rendered, err := render.SelectFrom(dialect.PostgreSQL(), users).
		Project(id, query.CountAll().As("total")).
		GroupByColumns("id").
		Having(query.GreaterThan(query.CountAll(), query.Bind(1))).
		Having(query.LessThan(query.CountAll(), query.Bind(100))).
		Build()
	require.NoError(t, err)
	require.Equal(t,
		`SELECT "users"."id", COUNT(*) AS "total" FROM "users" GROUP BY "users"."id" HAVING ((COUNT(*) > $1) AND (COUNT(*) < $2))`,
		rendered.SQL())
	require.Equal(t, []any{1, 100}, rendered.Args())

	viaExpression, err := render.SelectFrom(dialect.PostgreSQL(), users).
		Project(id, query.CountAll().As("total")).
		GroupBy(id).
		Build()
	require.NoError(t, err)
	require.Contains(t, viaExpression.SQL(), ` GROUP BY "users"."id"`)
}

// TestSelectBuilderGroupsByJoinedColumn pins the order in which Build assembles
// the statement. The grouping and the projections are validated together with
// the joins, so a joined table's column may be grouped by and projected. Adding
// the joins after the first validation refused both, because the statement
// validation judged did not yet select from the joined table.
func TestSelectBuilderGroupsByJoinedColumn(t *testing.T) {
	users := fluentUsers(t)
	orders, err := query.NewTableRef(schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	orderUserID, err := orders.Column("user_id")
	require.NoError(t, err)

	rendered, err := render.SelectFrom(dialect.PostgreSQL(), users).
		Project(orderUserID, query.CountAll().As("total")).
		Join(query.InnerJoin(orders, query.Equal(id, orderUserID))).
		GroupBy(orderUserID).
		Having(query.GreaterThan(query.CountAll(), query.Bind(1))).
		Build()
	require.NoError(t, err)
	require.Equal(t,
		`SELECT "orders"."user_id", COUNT(*) AS "total" FROM "users" INNER JOIN "orders" ON ("users"."id" = "orders"."user_id") GROUP BY "orders"."user_id" HAVING (COUNT(*) > $1)`,
		rendered.SQL())
	require.Equal(t, []any{1}, rendered.Args())

	// GroupByColumns names a primary-table column, so grouping by it beside a
	// joined projection has to survive the same assembly order.
	mixed, err := render.SelectFrom(dialect.PostgreSQL(), users).
		Project(id, orderUserID, query.CountAll().As("total")).
		Join(query.InnerJoin(orders, query.Equal(id, orderUserID))).
		GroupByColumns("id").
		GroupBy(orderUserID).
		Build()
	require.NoError(t, err)
	require.Contains(t, mixed.SQL(), ` GROUP BY "users"."id", "orders"."user_id"`)

	// The same ordering governs an ungrouped statement that projects a joined
	// table's column.
	projected, err := render.SelectFrom(dialect.PostgreSQL(), users).
		Project(id, orderUserID).
		Join(query.InnerJoin(orders, query.Equal(id, orderUserID))).
		Build()
	require.NoError(t, err)
	require.Equal(t,
		`SELECT "users"."id", "orders"."user_id" FROM "users" INNER JOIN "orders" ON ("users"."id" = "orders"."user_id")`,
		projected.SQL())

	// A grouping expression that reads a table the statement never selects from
	// is still refused.
	_, err = render.SelectFrom(dialect.PostgreSQL(), users).
		Project(orderUserID, query.CountAll().As("total")).
		GroupBy(orderUserID).
		Build()
	require.ErrorContains(t, err, `references table "orders" outside the statement`)
}

func TestSelectBuilderGroupingIsImmutable(t *testing.T) {
	users := fluentUsers(t)
	id, err := users.Column("id")
	require.NoError(t, err)

	base := render.SelectFrom(dialect.PostgreSQL(), users).
		Project(id, query.CountAll().As("total")).
		GroupByColumns("id")
	havingAdded := base.Having(query.GreaterThan(query.CountAll(), query.Bind(1)))

	baseStatement, err := base.Build()
	require.NoError(t, err)
	require.NotContains(t, baseStatement.SQL(), " HAVING ")
	havingStatement, err := havingAdded.Build()
	require.NoError(t, err)
	require.Contains(t, havingStatement.SQL(), " HAVING ")

	// A second GroupBy call on the original builder must not reach a copy
	// already taken from it, matching TestSelectBuilderIsImmutable.
	moreGrouping := base.GroupBy(id)
	baseAgain, err := base.Build()
	require.NoError(t, err)
	require.Equal(t, baseStatement.SQL(), baseAgain.SQL())
	moreGroupingStatement, err := moreGrouping.Build()
	require.NoError(t, err)
	require.NotEqual(t, baseStatement.SQL(), moreGroupingStatement.SQL())
}

func TestSelectBuilderRejectsCountWithGrouping(t *testing.T) {
	users := fluentUsers(t)

	_, err := render.SelectFrom(dialect.PostgreSQL(), users).GroupByColumns("id").BuildCount()
	require.Error(t, err)
	require.ErrorContains(t, err, "cannot count a grouped statement")

	_, err = render.SelectFrom(dialect.PostgreSQL(), users).
		Having(query.GreaterThan(query.CountAll(), query.Bind(1))).
		BuildCount()
	require.Error(t, err)
	require.ErrorContains(t, err, "cannot count a statement with a HAVING clause")
}

func TestSelectBuilderRejectsCountWithPaging(t *testing.T) {
	users := fluentUsers(t)

	_, err := render.SelectFrom(dialect.PostgreSQL(), users).Select("id").Limit(10).BuildCount()
	require.Error(t, err)

	_, err = render.SelectFrom(dialect.PostgreSQL(), users).Select("id").Offset(5).BuildCount()
	require.Error(t, err)
}

func TestSelectBuilderBuildCountReportsBuildErrors(t *testing.T) {
	users := fluentUsers(t)

	_, err := render.SelectFrom(dialect.PostgreSQL(), users).Select("missing").BuildCount()
	require.Error(t, err)
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
	_, err = render.SelectFrom(dialect.PostgreSQL(), users).WhereIn("id").Build()
	require.Error(t, err)
	_, err = render.SelectFrom(dialect.PostgreSQL(), users).WhereIn("missing", 1).Build()
	require.Error(t, err)
	_, err = render.SelectFrom(dialect.PostgreSQL(), users).GroupByColumns("missing").Build()
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

	t.Run("WhereIn joins the accumulated predicates", func(t *testing.T) {
		statement, err := render.SelectFrom(dialect.PostgreSQL(), users).
			Select("id").
			Where(query.Like(email, query.Bind("%@example.com"))).
			WhereIn("id", 1, 2).
			Build()
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id" FROM "users" WHERE (("users"."email" LIKE $1) AND ("users"."id" IN ($2, $3)))`,
			statement.SQL())
		require.Equal(t, []any{"%@example.com", 1, 2}, statement.Args())
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

	t.Run("BuildCount carries every accumulated predicate", func(t *testing.T) {
		// BuildCount replaces the projections but must count exactly the rows
		// Build would return, so it has to combine the accumulated predicates
		// the same way Build does rather than keep only one of them.
		builder := render.SelectFrom(dialect.PostgreSQL(), users).
			Select("id").
			WhereEqual("id", 42).
			Where(query.Like(email, query.Bind("%@example.com"))).
			Where(query.IsNotNull(email))

		counted, err := builder.BuildCount()
		require.NoError(t, err)
		require.Equal(t,
			`SELECT COUNT(*) AS "count" FROM "users" WHERE (("users"."id" = $1) AND ("users"."email" LIKE $2) AND ("users"."email" IS NOT NULL))`,
			counted.SQL())
		require.Equal(t, []any{42, "%@example.com"}, counted.Args())

		selected, err := builder.Build()
		require.NoError(t, err)
		require.Equal(t, selected.Args(), counted.Args())
	})

	t.Run("BuildCount carries a membership predicate", func(t *testing.T) {
		// A membership predicate binds one placeholder per value, so counting it
		// has to carry every one of them into the counted statement.
		counted, err := render.SelectFrom(dialect.PostgreSQL(), users).
			Select("id").
			WhereIn("id", 1, 2, 3).
			BuildCount()
		require.NoError(t, err)
		require.Equal(t,
			`SELECT COUNT(*) AS "count" FROM "users" WHERE ("users"."id" IN ($1, $2, $3))`,
			counted.SQL())
		require.Equal(t, []any{1, 2, 3}, counted.Args())
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

	t.Run("Or then WhereEqual nests correctly", func(t *testing.T) {
		statement, err := render.SelectFrom(dialect.PostgreSQL(), users).
			Select("id").
			Where(query.Or(
				query.Equal(email, query.Bind("ada@example.com")),
				query.Equal(email, query.Bind("bob@example.com")),
			)).
			WhereEqual("id", 42).
			Build()
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id" FROM "users" WHERE ((("users"."email" = $1) OR ("users"."email" = $2)) AND ("users"."id" = $3))`,
			statement.SQL())
		require.Equal(t, []any{"ada@example.com", "bob@example.com", 42}, statement.Args())
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

func fluentUsers(t *testing.T) query.TableRef {
	t.Helper()
	users, err := query.NewTableRef(schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	return users
}
