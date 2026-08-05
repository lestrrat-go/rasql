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
