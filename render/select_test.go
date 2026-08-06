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
