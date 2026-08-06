package render_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestSelectRendersForBuiltInDialects(t *testing.T) {
	statement := selectStatement(t)
	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     "SELECT \"u\".\"id\" AS \"user_id\" FROM \"users\" AS \"u\" INNER JOIN \"orders\" AS \"o\" ON (\"u\".\"id\" = \"o\".\"user_id\") WHERE ((\"o\".\"amount\" > $1) AND (\"o\".\"user_id\" IS NOT NULL)) ORDER BY \"o\".\"amount\" DESC LIMIT $2 OFFSET $3",
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "SELECT `u`.`id` AS `user_id` FROM `users` AS `u` INNER JOIN `orders` AS `o` ON (`u`.`id` = `o`.`user_id`) WHERE ((`o`.`amount` > ?) AND (`o`.`user_id` IS NOT NULL)) ORDER BY `o`.`amount` DESC LIMIT ? OFFSET ?",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     "SELECT \"u\".\"id\" AS \"user_id\" FROM \"users\" AS \"u\" INNER JOIN \"orders\" AS \"o\" ON (\"u\".\"id\" = \"o\".\"user_id\") WHERE ((\"o\".\"amount\" > ?) AND (\"o\".\"user_id\" IS NOT NULL)) ORDER BY \"o\".\"amount\" DESC LIMIT ? OFFSET ?",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.Select(test.dialect, statement)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Equal(t, []any{100, 20, 10}, rendered.Args())
			require.NotContains(t, rendered.SQL(), "100")
		})
	}
}

// TestSelectRendersQualifiedJoin pins the exact rendered SQL for a select
// that joins a schema-qualified table to another schema-qualified table,
// across all three built-in dialects: quoting each of Schema and Name as a
// separate identifier needs no dialect-specific branch.
func TestSelectRendersQualifiedJoin(t *testing.T) {
	statement := qualifiedJoinSelectStatement(t)
	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     `SELECT "audit"."events"."id", "audit"."events"."action" FROM "audit"."events" INNER JOIN "tenant"."users" ON ("audit"."events"."user_id" = "tenant"."users"."id") WHERE ("tenant"."users"."email" = $1)`,
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "SELECT `audit`.`events`.`id`, `audit`.`events`.`action` FROM `audit`.`events` INNER JOIN `tenant`.`users` ON (`audit`.`events`.`user_id` = `tenant`.`users`.`id`) WHERE (`tenant`.`users`.`email` = ?)",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     `SELECT "audit"."events"."id", "audit"."events"."action" FROM "audit"."events" INNER JOIN "tenant"."users" ON ("audit"."events"."user_id" = "tenant"."users"."id") WHERE ("tenant"."users"."email" = ?)`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.Select(test.dialect, statement)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Equal(t, []any{"a@example.com"}, rendered.Args())
		})
	}
}

func qualifiedJoinSelectStatement(t *testing.T) query.Select {
	t.Helper()
	events, err := query.NewTable(schema.Table{
		Schema: "audit",
		Name:   "events",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
			{Name: "action", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	users, err := query.NewTable(schema.Table{
		Schema: "tenant",
		Name:   "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)

	eventsID, err := events.Column("id")
	require.NoError(t, err)
	eventsUserID, err := events.Column("user_id")
	require.NoError(t, err)
	eventsAction, err := events.Column("action")
	require.NoError(t, err)
	usersID, err := users.Column("id")
	require.NoError(t, err)
	usersEmail, err := users.Column("email")
	require.NoError(t, err)

	statement, err := query.NewJoinedSelect(events,
		[]query.Join{query.InnerJoin(users, query.Equal(eventsUserID, usersID))},
		nil,
		query.Project(eventsID), query.Project(eventsAction),
	)
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.Equal(usersEmail, query.Bind("a@example.com")))
	require.NoError(t, err)
	return statement
}

// TestSelectRendersAliasWithoutSchema pins that an alias replaces a
// qualified table's whole name in every position: FROM names the qualified
// table, but a column reference is qualified by the alias alone.
func TestSelectRendersAliasWithoutSchema(t *testing.T) {
	events, err := query.NewTable(schema.Table{
		Schema: "audit",
		Name:   "events",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	aliased, err := events.As("e")
	require.NoError(t, err)
	id, err := aliased.Column("id")
	require.NoError(t, err)

	statement, err := query.NewSelect(aliased, query.Project(id))
	require.NoError(t, err)

	rendered, err := render.Select(dialect.PostgreSQL(), statement)
	require.NoError(t, err)
	require.Equal(t, `SELECT "e"."id" FROM "audit"."events" AS "e"`, rendered.SQL())
}

// TestSelectRendersQualifiedTableInGroupedStatement covers #62's GROUP
// BY/HAVING render path over a qualified table: both clauses render through
// writeExpression, which the qualification change alters in exactly one
// case, so a three-part key must appear in both.
func TestSelectRendersQualifiedTableInGroupedStatement(t *testing.T) {
	events, err := query.NewTable(schema.Table{
		Schema: "audit",
		Name:   "events",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	userID, err := events.Column("user_id")
	require.NoError(t, err)

	statement, err := query.NewGroupedSelect(events, []query.Expression{userID},
		query.Project(userID),
		query.Project(query.CountAll()),
	)
	require.NoError(t, err)
	statement, err = statement.WithHaving(query.GreaterThan(query.CountAll(), query.Bind(1)))
	require.NoError(t, err)

	rendered, err := render.Select(dialect.PostgreSQL(), statement)
	require.NoError(t, err)
	require.Equal(
		t,
		`SELECT "audit"."events"."user_id", COUNT(*) FROM "audit"."events" GROUP BY "audit"."events"."user_id" HAVING (COUNT(*) > $1)`,
		rendered.SQL(),
	)
	require.Equal(t, []any{1}, rendered.Args())
}

// TestSelectRendersQualifiedTableInSubquery covers #63's subquery render
// path: a subquery recurses into writeSelect, so its FROM and its columns go
// through the same substitutions as a top-level statement.
func TestSelectRendersQualifiedTableInSubquery(t *testing.T) {
	events, err := query.NewTable(schema.Table{
		Schema: "audit",
		Name:   "events",
		Columns: []schema.Column{
			{Name: "user_id", Type: schema.TypeInteger},
		},
	})
	require.NoError(t, err)
	eventsUserID, err := events.Column("user_id")
	require.NoError(t, err)
	inner, err := query.NewSelect(events, query.Project(eventsUserID))
	require.NoError(t, err)

	users, err := query.NewTable(schema.Table{
		Schema: "tenant",
		Name:   "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	usersID, err := users.Column("id")
	require.NoError(t, err)

	outer, err := query.NewSelect(users, query.Project(usersID))
	require.NoError(t, err)
	outer, err = outer.WithWhere(query.InSelect(usersID, inner))
	require.NoError(t, err)

	rendered, err := render.Select(dialect.PostgreSQL(), outer)
	require.NoError(t, err)
	require.Equal(
		t,
		`SELECT "tenant"."users"."id" FROM "tenant"."users" WHERE ("tenant"."users"."id" IN (SELECT "audit"."events"."user_id" FROM "audit"."events"))`,
		rendered.SQL(),
	)
}

func TestSelectRendersAggregateFunctions(t *testing.T) {
	statement := aggregateSelectStatement(t)
	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     "SELECT COUNT(*) AS \"total\", MAX(\"orders\".\"amount\") AS \"top\" FROM \"orders\" WHERE (\"orders\".\"amount\" <> $1) ORDER BY COUNT(*) DESC",
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "SELECT COUNT(*) AS `total`, MAX(`orders`.`amount`) AS `top` FROM `orders` WHERE (`orders`.`amount` <> ?) ORDER BY COUNT(*) DESC",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     "SELECT COUNT(*) AS \"total\", MAX(\"orders\".\"amount\") AS \"top\" FROM \"orders\" WHERE (\"orders\".\"amount\" <> ?) ORDER BY COUNT(*) DESC",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.Select(test.dialect, statement)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Equal(t, []any{"done"}, rendered.Args())
			require.Contains(t, rendered.SQL(), "COUNT(*)")
			require.NotContains(t, rendered.SQL(), `"COUNT"`)
			require.NotContains(t, rendered.SQL(), "`COUNT`")
		})
	}
}

func aggregateSelectStatement(t *testing.T) query.Select {
	t.Helper()
	orders, err := query.NewTable(schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "amount", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	amount, err := orders.Column("amount")
	require.NoError(t, err)

	statement, err := query.NewSelect(orders,
		query.Project(query.CountAll()).As("total"),
		query.Project(query.Max(amount)).As("top"),
	)
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.NotEqual(amount, query.Bind("done")))
	require.NoError(t, err)
	// An aggregate-only projection may order by an aggregate, so the rendering
	// has to carry the call into ORDER BY for every dialect.
	statement, err = statement.WithOrder(query.Desc(query.CountAll()))
	require.NoError(t, err)
	return statement
}

// TestSelectRendersDistinctStatement proves SELECT DISTINCT renders right
// after SELECT, ahead of the projections, and composes with a join, a WHERE
// clause, ordering, and paging without changing the bound arguments.
func TestSelectRendersDistinctStatement(t *testing.T) {
	statement := distinctSelectStatement(t)
	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     `SELECT DISTINCT "u"."id" AS "user_id" FROM "users" AS "u" INNER JOIN "orders" AS "o" ON ("u"."id" = "o"."user_id") WHERE ("o"."amount" > $1) ORDER BY "u"."id" LIMIT $2 OFFSET $3`,
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "SELECT DISTINCT `u`.`id` AS `user_id` FROM `users` AS `u` INNER JOIN `orders` AS `o` ON (`u`.`id` = `o`.`user_id`) WHERE (`o`.`amount` > ?) ORDER BY `u`.`id` LIMIT ? OFFSET ?",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     `SELECT DISTINCT "u"."id" AS "user_id" FROM "users" AS "u" INNER JOIN "orders" AS "o" ON ("u"."id" = "o"."user_id") WHERE ("o"."amount" > ?) ORDER BY "u"."id" LIMIT ? OFFSET ?`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.Select(test.dialect, statement)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Equal(t, []any{100, 20, 10}, rendered.Args())
		})
	}
}

func distinctSelectStatement(t *testing.T) query.Select {
	t.Helper()
	users, err := query.NewTable(schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	users, err = users.As("u")
	require.NoError(t, err)
	orders, err := query.NewTable(schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
			{Name: "amount", Type: schema.TypeFloat},
		},
		PrimaryKey: []string{"id"},
	})
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
	statement, err = statement.WithDistinct()
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.GreaterThan(amount, query.Bind(100)))
	require.NoError(t, err)
	statement, err = statement.WithOrder(query.Asc(userID))
	require.NoError(t, err)
	statement, err = statement.WithLimit(20)
	require.NoError(t, err)
	statement, err = statement.WithOffset(10)
	require.NoError(t, err)
	return statement
}

// TestSelectRendersDistinctFunctionArgument proves Function.WithDistinct
// renders as COUNT(DISTINCT x), the modifier COUNT(DISTINCT x) needs now that
// BuildCount refuses a distinct SelectBuilder.
func TestSelectRendersDistinctFunctionArgument(t *testing.T) {
	orders, err := query.NewTable(schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	orderUserID, err := orders.Column("user_id")
	require.NoError(t, err)

	statement, err := query.NewSelect(orders, query.Project(query.Count(orderUserID).WithDistinct()).As("distinct_users"))
	require.NoError(t, err)

	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     `SELECT COUNT(DISTINCT "orders"."user_id") AS "distinct_users" FROM "orders"`,
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "SELECT COUNT(DISTINCT `orders`.`user_id`) AS `distinct_users` FROM `orders`",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     `SELECT COUNT(DISTINCT "orders"."user_id") AS "distinct_users" FROM "orders"`,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.Select(test.dialect, statement)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Empty(t, rendered.Args())
		})
	}
}

// TestSelectRendersDistinctSubquery proves a distinct subquery renders through
// the same writeSelect path as a top-level statement, in the shape of
// TestSelectRendersSubqueriesForBuiltInDialects, and that placeholder
// numbering carries through it unaffected, since DISTINCT binds no argument.
func TestSelectRendersDistinctSubquery(t *testing.T) {
	users, err := query.NewTable(schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "status", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	orders, err := query.NewTable(schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	userID, err := users.Column("id")
	require.NoError(t, err)
	status, err := users.Column("status")
	require.NoError(t, err)
	orderUserID, err := orders.Column("user_id")
	require.NoError(t, err)

	subquery, err := query.NewSelect(orders, query.Project(orderUserID))
	require.NoError(t, err)
	subquery, err = subquery.WithDistinct()
	require.NoError(t, err)

	statement, err := query.NewSelect(users, query.Project(userID))
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.And(
		query.Equal(status, query.Bind("active")),
		query.InSelect(userID, subquery),
	))
	require.NoError(t, err)

	rendered, err := render.Select(dialect.PostgreSQL(), statement)
	require.NoError(t, err)
	require.Equal(t,
		`SELECT "users"."id" FROM "users" WHERE (("users"."status" = $1) AND ("users"."id" IN (SELECT DISTINCT "orders"."user_id" FROM "orders")))`,
		rendered.SQL())
	require.Equal(t, []any{"active"}, rendered.Args())
}

// TestSelectRendersScalarFunctions proves COALESCE, LOWER, UPPER, and ABS
// render as ordinary function calls with no dialect-specific branch, and
// pins placeholder numbering with a bound argument sitting inside COALESCE
// between two other binds.
func TestSelectRendersScalarFunctions(t *testing.T) {
	users, err := query.NewTable(schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
			{Name: "score", Type: schema.TypeInteger, Nullable: true},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)
	score, err := users.Column("score")
	require.NoError(t, err)

	statement, err := query.NewSelect(users,
		query.Project(query.Lower(email)).As("lower_email"),
		query.Project(query.Upper(email)).As("upper_email"),
		query.Project(query.Abs(score)).As("abs_score"),
	)
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.Equal(
		query.Coalesce(query.Bind(1), score, query.Bind(2)),
		query.Bind(3),
	))
	require.NoError(t, err)

	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     "SELECT LOWER(\"users\".\"email\") AS \"lower_email\", UPPER(\"users\".\"email\") AS \"upper_email\", ABS(\"users\".\"score\") AS \"abs_score\" FROM \"users\" WHERE (COALESCE($1, \"users\".\"score\", $2) = $3)",
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "SELECT LOWER(`users`.`email`) AS `lower_email`, UPPER(`users`.`email`) AS `upper_email`, ABS(`users`.`score`) AS `abs_score` FROM `users` WHERE (COALESCE(?, `users`.`score`, ?) = ?)",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     "SELECT LOWER(\"users\".\"email\") AS \"lower_email\", UPPER(\"users\".\"email\") AS \"upper_email\", ABS(\"users\".\"score\") AS \"abs_score\" FROM \"users\" WHERE (COALESCE(?, \"users\".\"score\", ?) = ?)",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.Select(test.dialect, statement)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Equal(t, []any{1, 2, 3}, rendered.Args())
		})
	}
}

// TestSelectRendersScalarFunctionEscapeHatch proves Func renders its
// caller-supplied name raw, with no quoting, exactly like a curated function
// name, once validation has confirmed it is a legal identifier.
func TestSelectRendersScalarFunctionEscapeHatch(t *testing.T) {
	users, err := query.NewTable(schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "doc", Type: schema.TypeJSON},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	doc, err := users.Column("doc")
	require.NoError(t, err)

	statement, err := query.NewSelect(users, query.Project(query.Func("jsonb_path_query", doc, query.Bind("$.a"))).As("matched"))
	require.NoError(t, err)

	rendered, err := render.Select(dialect.PostgreSQL(), statement)
	require.NoError(t, err)
	require.Equal(t, `SELECT jsonb_path_query("users"."doc", $1) AS "matched" FROM "users"`, rendered.SQL())
	require.Equal(t, []any{"$.a"}, rendered.Args())
}

// TestSelectRendersDistinctEscapeHatchArgument proves a Func call carries
// WithDistinct into the rendered SQL, which is how an aggregate this package
// does not curate is called with DISTINCT. Validation refuses the same
// modifier on a curated scalar call, so this path is the only one that reaches
// the renderer with a non-aggregate name.
func TestSelectRendersDistinctEscapeHatchArgument(t *testing.T) {
	posts, err := query.NewTable(schema.Table{
		Name: "posts",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "tag", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	tag, err := posts.Column("tag")
	require.NoError(t, err)

	statement, err := query.NewSelect(posts, query.Project(query.Func("group_concat", tag).WithDistinct()).As("tags"))
	require.NoError(t, err)

	rendered, err := render.Select(dialect.SQLite(), statement)
	require.NoError(t, err)
	require.Equal(t, `SELECT group_concat(DISTINCT "posts"."tag") AS "tags" FROM "posts"`, rendered.SQL())
	require.Empty(t, rendered.Args())
}

// TestSelectRendersGroupedStatement proves GROUP BY and HAVING render in the
// clause order SQL requires, between WHERE and ORDER BY, identically across all
// three dialects except for identifier quoting and placeholder style.
func TestSelectRendersGroupedStatement(t *testing.T) {
	statement := groupedSelectStatement(t)
	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     `SELECT "tasks"."status", COUNT(*) AS "total" FROM "tasks" WHERE ("tasks"."status" <> $1) GROUP BY "tasks"."status" HAVING (COUNT(*) > $2) ORDER BY COUNT(*) DESC`,
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "SELECT `tasks`.`status`, COUNT(*) AS `total` FROM `tasks` WHERE (`tasks`.`status` <> ?) GROUP BY `tasks`.`status` HAVING (COUNT(*) > ?) ORDER BY COUNT(*) DESC",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     `SELECT "tasks"."status", COUNT(*) AS "total" FROM "tasks" WHERE ("tasks"."status" <> ?) GROUP BY "tasks"."status" HAVING (COUNT(*) > ?) ORDER BY COUNT(*) DESC`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.Select(test.dialect, statement)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Equal(t, []any{"done", 1}, rendered.Args())
		})
	}
}

func groupedSelectStatement(t *testing.T) query.Select {
	t.Helper()
	tasks, err := query.NewTable(schema.Table{
		Name: "tasks",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "status", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	status, err := tasks.Column("status")
	require.NoError(t, err)

	statement, err := query.NewGroupedSelect(tasks, []query.Expression{status},
		query.Project(status),
		query.Project(query.CountAll()).As("total"),
	)
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.NotEqual(status, query.Bind("done")))
	require.NoError(t, err)
	statement, err = statement.WithHaving(query.GreaterThan(query.CountAll(), query.Bind(1)))
	require.NoError(t, err)
	// A grouped statement is one group per key, so ORDER BY may call an
	// aggregate exactly as it may over an aggregate-only projection set.
	statement, err = statement.WithOrder(query.Desc(query.CountAll()))
	require.NoError(t, err)
	return statement
}

// TestSelectRendersSubqueryInGroupedClauses proves the two clauses this package
// gained last render a subquery like every other SELECT clause: the subquery's
// own arguments join the enclosing statement's list at the position the clause
// occupies, so placeholder numbering stays correct across GROUP BY and HAVING.
func TestSelectRendersSubqueryInGroupedClauses(t *testing.T) {
	tasks, err := query.NewTable(schema.Table{
		Name: "tasks",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "priority", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	priority, err := tasks.Column("priority")
	require.NoError(t, err)
	allTasks, err := tasks.As("all_tasks")
	require.NoError(t, err)
	allPriority, err := allTasks.Column("priority")
	require.NoError(t, err)

	average, err := query.NewSelect(allTasks, query.Project(query.Avg(allPriority)))
	require.NoError(t, err)
	average, err = average.WithWhere(query.GreaterThan(allPriority, query.Bind(0)))
	require.NoError(t, err)

	statement, err := query.NewGroupedSelect(tasks,
		[]query.Expression{query.GreaterThan(priority, query.Scalar(average))},
		query.Project(query.CountAll()).As("total"),
	)
	require.NoError(t, err)
	statement, err = statement.WithHaving(query.GreaterThan(query.CountAll(), query.Bind(1)))
	require.NoError(t, err)

	rendered, err := render.Select(dialect.PostgreSQL(), statement)
	require.NoError(t, err)
	require.Equal(
		t,
		`SELECT COUNT(*) AS "total" FROM "tasks" GROUP BY ("tasks"."priority" > (SELECT AVG("all_tasks"."priority") FROM "tasks" AS "all_tasks" WHERE ("all_tasks"."priority" > $1))) HAVING (COUNT(*) > $2)`,
		rendered.SQL(),
	)
	require.Equal(t, []any{0, 1}, rendered.Args())
}

func TestSelectRejectsNilDialect(t *testing.T) {
	_, err := render.Select(nil, selectStatement(t))
	require.Error(t, err)
}

func TestSelectRendersMembershipForBuiltInDialects(t *testing.T) {
	statement := membershipStatement(t)
	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     "SELECT \"users\".\"id\", \"users\".\"email\" FROM \"users\" WHERE ((\"users\".\"id\" IN ($1, $2, $3)) AND (\"users\".\"email\" NOT IN ($4, $5)) AND (\"users\".\"id\" > $6))",
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "SELECT `users`.`id`, `users`.`email` FROM `users` WHERE ((`users`.`id` IN (?, ?, ?)) AND (`users`.`email` NOT IN (?, ?)) AND (`users`.`id` > ?))",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     "SELECT \"users\".\"id\", \"users\".\"email\" FROM \"users\" WHERE ((\"users\".\"id\" IN (?, ?, ?)) AND (\"users\".\"email\" NOT IN (?, ?)) AND (\"users\".\"id\" > ?))",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.Select(test.dialect, statement)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Equal(t, []any{1, 2, 3, "ada@example.com", "bob@example.com", 10}, rendered.Args())
			require.NotContains(t, rendered.SQL(), "ada@example.com")
		})
	}
}

func TestSelectRendersMembershipWithColumnMember(t *testing.T) {
	statement := columnMembershipStatement(t)
	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     "SELECT \"users\".\"id\" FROM \"users\" INNER JOIN \"orders\" ON (\"users\".\"id\" = \"orders\".\"user_id\") WHERE ((\"users\".\"id\" IN (\"orders\".\"user_id\")) AND (\"users\".\"id\" NOT IN ($1, \"orders\".\"id\")))",
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "SELECT `users`.`id` FROM `users` INNER JOIN `orders` ON (`users`.`id` = `orders`.`user_id`) WHERE ((`users`.`id` IN (`orders`.`user_id`)) AND (`users`.`id` NOT IN (?, `orders`.`id`)))",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     "SELECT \"users\".\"id\" FROM \"users\" INNER JOIN \"orders\" ON (\"users\".\"id\" = \"orders\".\"user_id\") WHERE ((\"users\".\"id\" IN (\"orders\".\"user_id\")) AND (\"users\".\"id\" NOT IN (?, \"orders\".\"id\")))",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.Select(test.dialect, statement)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			// A column member renders as a quoted identifier and contributes no
			// argument, so only the one bound value shows up in Args.
			require.Equal(t, []any{7}, rendered.Args())
		})
	}
}

// TestSelectRendersSubqueriesForBuiltInDialects pins the worked example from the
// design: an InSelect predicate that costs no per-candidate argument, and a
// Scalar comparison against an aggregate over an aliased self-subquery.
func TestSelectRendersSubqueriesForBuiltInDialects(t *testing.T) {
	statement := subqueryStatement(t)
	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     `SELECT "tasks"."id", "tasks"."title" FROM "tasks" WHERE (("tasks"."project_id" IN (SELECT "projects"."id" FROM "projects" WHERE ("projects"."owner_id" = $1))) AND ("tasks"."priority" >= (SELECT AVG("all_tasks"."priority") FROM "tasks" AS "all_tasks")))`,
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "SELECT `tasks`.`id`, `tasks`.`title` FROM `tasks` WHERE ((`tasks`.`project_id` IN (SELECT `projects`.`id` FROM `projects` WHERE (`projects`.`owner_id` = ?))) AND (`tasks`.`priority` >= (SELECT AVG(`all_tasks`.`priority`) FROM `tasks` AS `all_tasks`)))",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     `SELECT "tasks"."id", "tasks"."title" FROM "tasks" WHERE (("tasks"."project_id" IN (SELECT "projects"."id" FROM "projects" WHERE ("projects"."owner_id" = ?))) AND ("tasks"."priority" >= (SELECT AVG("all_tasks"."priority") FROM "tasks" AS "all_tasks")))`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.Select(test.dialect, statement)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Equal(t, []any{7}, rendered.Args())
			require.NotContains(t, rendered.SQL(), "7")
		})
	}
}

// TestSelectRendersSubqueryPlaceholdersInOrder pins that a nested statement
// shares the outer renderer, so a bound value before the subquery, bound values
// inside it, and a bound value after it all number from one contiguous sequence.
func TestSelectRendersSubqueryPlaceholdersInOrder(t *testing.T) {
	users, err := query.NewTable(schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "status", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	orders, err := query.NewTable(schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
			{Name: "amount", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	userID, err := users.Column("id")
	require.NoError(t, err)
	status, err := users.Column("status")
	require.NoError(t, err)
	orderUserID, err := orders.Column("user_id")
	require.NoError(t, err)
	amount, err := orders.Column("amount")
	require.NoError(t, err)

	subquery, err := query.NewSelect(orders, query.Project(orderUserID))
	require.NoError(t, err)
	subquery, err = subquery.WithWhere(query.GreaterThan(amount, query.Bind(2)))
	require.NoError(t, err)

	statement, err := query.NewSelect(users, query.Project(userID))
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.And(
		query.Equal(status, query.Bind(1)),
		query.InSelect(userID, subquery),
		query.GreaterThan(userID, query.Bind(3)),
	))
	require.NoError(t, err)

	rendered, err := render.Select(dialect.PostgreSQL(), statement)
	require.NoError(t, err)
	require.Equal(t,
		`SELECT "users"."id" FROM "users" WHERE (("users"."status" = $1) AND ("users"."id" IN (SELECT "orders"."user_id" FROM "orders" WHERE ("orders"."amount" > $2))) AND ("users"."id" > $3))`,
		rendered.SQL())
	require.Equal(t, []any{1, 2, 3}, rendered.Args())
}

// TestSelectRejectsLimitedSubqueryInMembership pins the one dialect difference
// this change renders: MySQL refuses a LIMIT or OFFSET on the right-hand side of
// IN (error 1235), while PostgreSQL and SQLite accept it. A Scalar subquery
// carries no such restriction on any of the three, since it is never combined
// with IN.
func TestSelectRejectsLimitedSubqueryInMembership(t *testing.T) {
	users, err := query.NewTable(schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	orders, err := query.NewTable(schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	userID, err := users.Column("id")
	require.NoError(t, err)
	orderUserID, err := orders.Column("user_id")
	require.NoError(t, err)

	limited, err := query.NewSelect(orders, query.Project(orderUserID))
	require.NoError(t, err)
	limited, err = limited.WithLimit(1)
	require.NoError(t, err)

	statement, err := query.NewSelect(users, query.Project(userID))
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.InSelect(userID, limited))
	require.NoError(t, err)

	for _, d := range []dialect.Dialect{dialect.PostgreSQL(), dialect.SQLite()} {
		t.Run(d.Name(), func(t *testing.T) {
			_, err := render.Select(d, statement)
			require.NoError(t, err)
		})
	}
	t.Run("mysql", func(t *testing.T) {
		_, err := render.Select(dialect.MySQL(), statement)
		require.Error(t, err)
		require.ErrorContains(t, err, "mysql")
		require.ErrorContains(t, err, "LIMIT")
	})

	scalarStatement, err := query.NewSelect(users, query.Project(userID))
	require.NoError(t, err)
	scalarStatement, err = scalarStatement.WithWhere(query.GreaterThanOrEqual(userID, query.Scalar(limited)))
	require.NoError(t, err)
	for _, d := range []dialect.Dialect{dialect.PostgreSQL(), dialect.MySQL(), dialect.SQLite()} {
		t.Run(d.Name()+" scalar", func(t *testing.T) {
			_, err := render.Select(d, scalarStatement)
			require.NoError(t, err)
		})
	}
}

// subqueryStatement builds the worked example from the design: every task in a
// project the caller owns, whose priority is at or above the average priority
// of all tasks. all_tasks is an alias so the average subquery is a separate
// scope from the outer statement even though it names the same table.
func subqueryStatement(t *testing.T) query.Select {
	t.Helper()
	tasksDefinition := schema.Table{
		Name: "tasks",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "project_id", Type: schema.TypeInteger},
			{Name: "title", Type: schema.TypeText},
			{Name: "priority", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	}
	tasks, err := query.NewTable(tasksDefinition)
	require.NoError(t, err)
	projects, err := query.NewTable(schema.Table{
		Name: "projects",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "owner_id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	taskID, err := tasks.Column("id")
	require.NoError(t, err)
	taskProjectID, err := tasks.Column("project_id")
	require.NoError(t, err)
	taskTitle, err := tasks.Column("title")
	require.NoError(t, err)
	taskPriority, err := tasks.Column("priority")
	require.NoError(t, err)
	projectID, err := projects.Column("id")
	require.NoError(t, err)
	projectOwnerID, err := projects.Column("owner_id")
	require.NoError(t, err)

	owned, err := query.NewSelect(projects, query.Project(projectID))
	require.NoError(t, err)
	owned, err = owned.WithWhere(query.Equal(projectOwnerID, query.Bind(7)))
	require.NoError(t, err)

	allTasks, err := tasks.As("all_tasks")
	require.NoError(t, err)
	allTasksPriority, err := allTasks.Column("priority")
	require.NoError(t, err)
	average, err := query.NewSelect(allTasks, query.Project(query.Avg(allTasksPriority)))
	require.NoError(t, err)

	statement, err := query.NewSelect(tasks, query.Project(taskID), query.Project(taskTitle))
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.And(
		query.InSelect(taskProjectID, owned),
		query.GreaterThanOrEqual(taskPriority, query.Scalar(average)),
	))
	require.NoError(t, err)
	return statement
}

// columnMembershipStatement builds a statement whose membership predicates take
// a column as a member, which is valid SQL and is accepted by design.
func columnMembershipStatement(t *testing.T) query.Select {
	t.Helper()
	users, err := query.NewTable(schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	orders, err := query.NewTable(schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	userID, err := users.Column("id")
	require.NoError(t, err)
	orderID, err := orders.Column("id")
	require.NoError(t, err)
	orderUserID, err := orders.Column("user_id")
	require.NoError(t, err)

	statement, err := query.NewSelect(users, query.Project(userID))
	require.NoError(t, err)
	statement, err = statement.WithJoin(query.InnerJoin(orders, query.Equal(userID, orderUserID)))
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.And(
		query.In(userID, orderUserID),
		query.NotIn(userID, query.Bind(7), orderID),
	))
	require.NoError(t, err)
	return statement
}

func membershipStatement(t *testing.T) query.Select {
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
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)

	statement, err := query.NewSelect(users, query.Project(id), query.Project(email))
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.And(
		query.In(id, query.Bind(1), query.Bind(2), query.Bind(3)),
		query.NotIn(email, query.Bind("ada@example.com"), query.Bind("bob@example.com")),
		query.GreaterThan(id, query.Bind(10)),
	))
	require.NoError(t, err)
	return statement
}

func selectStatement(t *testing.T) query.Select {
	t.Helper()
	users, err := query.NewTable(schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	users, err = users.As("u")
	require.NoError(t, err)
	orders, err := query.NewTable(schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
			{Name: "amount", Type: schema.TypeFloat},
		},
		PrimaryKey: []string{"id"},
	})
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
	return statement
}
