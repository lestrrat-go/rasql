package render_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// existsFixture is the users/orders pair the tests below render against.
type existsFixture struct {
	users      query.TableRef
	usersID    query.ColumnRef
	orders     query.TableRef
	ordersUser query.ColumnRef
}

func newExistsFixture(t *testing.T) existsFixture {
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
	orders, err := query.NewTableRef(schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	return existsFixture{
		users:      users,
		usersID:    users.Column("id"),
		orders:     orders,
		ordersUser: orders.Column("user_id"),
	}
}

// ordersOfUser is the correlated SELECT 1 the tests reuse.
func (f existsFixture) ordersOfUser(t *testing.T) query.Select {
	t.Helper()

	statement, err := query.NewSelect(f.orders, query.Project(query.Bind(1)))
	require.NoError(t, err)
	statement, err = statement.WithCorrelation(f.users)
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.Equal(f.ordersUser, f.usersID))
	require.NoError(t, err)
	return statement
}

// TestSelectRendersExists pins the SQL text of both existence tests on every
// built-in dialect, including where the subquery's own bound value lands in the
// argument list and how each dialect numbers the placeholder for it.
func TestSelectRendersExists(t *testing.T) {
	f := newExistsFixture(t)

	for name, test := range map[string]struct {
		predicate query.Expression
		sql       map[string]string
	}{
		"exists": {
			predicate: query.Exists(f.ordersOfUser(t)),
			sql: map[string]string{
				"postgresql": `SELECT "users"."id" FROM "users" WHERE (EXISTS (SELECT $1 FROM "orders" WHERE ("orders"."user_id" = "users"."id")))`,
				"mysql":      "SELECT `users`.`id` FROM `users` WHERE (EXISTS (SELECT ? FROM `orders` WHERE (`orders`.`user_id` = `users`.`id`)))",
				"sqlite":     `SELECT "users"."id" FROM "users" WHERE (EXISTS (SELECT ? FROM "orders" WHERE ("orders"."user_id" = "users"."id")))`,
			},
		},
		"not exists": {
			predicate: query.NotExists(f.ordersOfUser(t)),
			sql: map[string]string{
				"postgresql": `SELECT "users"."id" FROM "users" WHERE (NOT EXISTS (SELECT $1 FROM "orders" WHERE ("orders"."user_id" = "users"."id")))`,
				"mysql":      "SELECT `users`.`id` FROM `users` WHERE (NOT EXISTS (SELECT ? FROM `orders` WHERE (`orders`.`user_id` = `users`.`id`)))",
				"sqlite":     `SELECT "users"."id" FROM "users" WHERE (NOT EXISTS (SELECT ? FROM "orders" WHERE ("orders"."user_id" = "users"."id")))`,
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			statement, err := query.NewSelect(f.users, f.usersID)
			require.NoError(t, err)
			statement, err = statement.WithWhere(test.predicate)
			require.NoError(t, err)

			for dialectName, d := range map[string]dialect.Dialect{
				"postgresql": dialect.PostgreSQL(),
				"mysql":      dialect.MySQL(),
				"sqlite":     dialect.SQLite(),
			} {
				t.Run(dialectName, func(t *testing.T) {
					rendered, err := render.Select(d, statement)
					require.NoError(t, err)
					require.Equal(t, test.sql[dialectName], rendered.SQL())
					require.Equal(t, []any{1}, rendered.Args())
				})
			}
		})
	}
}

// TestSelectRendersExistsWithLimit pins that no dialect capability gates a
// LIMIT inside EXISTS, unlike the one that gates a LIMIT inside IN (SELECT …).
// MySQL's error 1235 names a LIMIT in an IN/ALL/ANY/SOME subquery, and MySQL
// runs EXISTS (SELECT … LIMIT 1); ExistsLimitAgainstLiveDatabases in the root
// package is what confirms that against the servers themselves.
func TestSelectRendersExistsWithLimit(t *testing.T) {
	f := newExistsFixture(t)

	subquery, err := f.ordersOfUser(t).WithLimit(1)
	require.NoError(t, err)
	statement, err := query.NewSelect(f.users, f.usersID)
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.Exists(subquery))
	require.NoError(t, err)

	rendered, err := render.Select(dialect.MySQL(), statement)
	require.NoError(t, err)
	require.Equal(
		t,
		"SELECT `users`.`id` FROM `users` WHERE (EXISTS (SELECT ? FROM `orders` WHERE (`orders`.`user_id` = `users`.`id`) LIMIT ?))",
		rendered.SQL(),
	)
	require.Equal(t, []any{1, 1}, rendered.Args())
}

// TestSelectRefusesACorrelatedStatementOnItsOwn pins the division between
// validation and rendering. A statement that declares a correlation is a
// consistent model of a subquery, so query.Validate accepts it; rendering it on
// its own would emit SQL reading a table the FROM clause never lists, so this
// package refuses it there instead.
func TestSelectRefusesACorrelatedStatementOnItsOwn(t *testing.T) {
	f := newExistsFixture(t)

	subquery := f.ordersOfUser(t)
	require.NoError(t, subquery.Validate())

	_, err := render.Select(dialect.PostgreSQL(), subquery)
	require.Error(t, err)
	require.ErrorContains(t, err, `declares a correlation with table "users"`)
	require.ErrorContains(t, err, "rendered on its own")
}
