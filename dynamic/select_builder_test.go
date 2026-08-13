package dynamic_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/dynamic"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestSelectBuilder(t *testing.T) {
	t.Run("repeated WhereEqual combines with AND", func(t *testing.T) {
		users := dynamicUsersTable(t)
		db := dbForBuild(t)
		statement, err := dynamic.SelectFrom(users).
			Select("id", "email").
			WhereEqual("id", 42).
			WhereEqual("email", "ada@example.com").
			Build(db.Dialect())
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id", "users"."email" FROM "users" WHERE (("users"."id" = $1) AND ("users"."email" = $2))`,
			statement.SQL())
		require.Equal(t, []any{42, "ada@example.com"}, statement.Args())
	})

	t.Run("Where after WhereEqual combines with AND", func(t *testing.T) {
		users := dynamicUsersTable(t)
		db := dbForBuild(t)
		email, err := users.Column("email")
		require.NoError(t, err)
		statement, err := dynamic.SelectFrom(users).
			Select("id", "email").
			WhereEqual("id", 42).
			Where(query.Equal(email, query.Bind("ada@example.com"))).
			Build(db.Dialect())
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id", "users"."email" FROM "users" WHERE (("users"."id" = $1) AND ("users"."email" = $2))`,
			statement.SQL())
		require.Equal(t, []any{42, "ada@example.com"}, statement.Args())
	})

	t.Run("a lone Where is unchanged", func(t *testing.T) {
		users := dynamicUsersTable(t)
		db := dbForBuild(t)
		statement, err := dynamic.SelectFrom(users).
			Select("id", "email").
			WhereEqual("id", 42).
			Build(db.Dialect())
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id", "users"."email" FROM "users" WHERE ("users"."id" = $1)`,
			statement.SQL())
		require.Equal(t, []any{42}, statement.Args())
	})

	t.Run("Distinct de-duplicates result rows", func(t *testing.T) {
		users := dynamicUsersTable(t)
		db := dbForBuild(t)
		statement, err := dynamic.SelectFrom(users).
			Select("email").
			Distinct().
			Build(db.Dialect())
		require.NoError(t, err)
		require.Equal(t, `SELECT DISTINCT "users"."email" FROM "users"`, statement.SQL())

		_, err = dynamic.SelectFrom(users).
			Select("email").
			Distinct().
			Count(t.Context(), db)
		require.Error(t, err)
		require.ErrorContains(t, err, "cannot count a distinct statement")
	})

	// The grouping is validated together with the joins, so a joined table's
	// column may be grouped by. Attaching the joins after the first validation
	// refused it.
	t.Run("GroupBy accepts a joined table's column", func(t *testing.T) {
		users := dynamicUsersTable(t)
		orders := dynamicOrdersTable(t)
		id, err := users.Column("id")
		require.NoError(t, err)
		orderUserID, err := orders.Column("user_id")
		require.NoError(t, err)

		statement, err := dynamic.SelectFrom(users).
			Project(query.Project(orderUserID), query.Project(query.CountAll()).As("total")).
			Join(query.InnerJoin(orders, query.Equal(id, orderUserID))).
			GroupBy(orderUserID).
			Build(dbForBuild(t).Dialect())
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "orders"."user_id", COUNT(*) AS "total" FROM "users" INNER JOIN "orders" ON ("users"."id" = "orders"."user_id") GROUP BY "orders"."user_id"`,
			statement.SQL())
	})
}

// dynamicUsersTable returns a table shaped like the generated users table the
// rest of this package's tests target, for a caller with no Go row type.
func dynamicUsersTable(t *testing.T) query.TableRef {
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

// dynamicOrdersTable returns a table to join dynamicUsersTable against.
func dynamicOrdersTable(t *testing.T) query.TableRef {
	t.Helper()

	orders, err := query.NewTableRef(schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	return orders
}

// dbForBuild returns a DB that renders statements without executing them.
func dbForBuild(t *testing.T) rasql.DB {
	t.Helper()

	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
	})
	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	return db
}
