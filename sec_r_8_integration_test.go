//go:build unix

package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestSQLiteRejectsCaseOnlyCorrelationAliasInReadsAndWrites(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	database.SetMaxOpenConns(1)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE users (id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER)`)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `INSERT INTO users (id) VALUES (1), (2)`)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `INSERT INTO orders (id, user_id) VALUES (1, 1)`)
	require.NoError(t, err)

	users := query.MustTableRef(schema.TableDef{
		Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	})
	orders := query.MustTableRef(schema.TableDef{
		Name: "orders", Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}}, {Name: "user_id", Type: schema.IntegerType{}},
		}, PrimaryKey: []string{"id"},
	})
	aliased, err := orders.As("USERS")
	require.NoError(t, err)
	inner, err := query.NewSelect(aliased, query.Project(query.Bind(1)))
	require.NoError(t, err)
	inner, err = inner.WithCorrelation(users)
	require.NoError(t, err)
	inner, err = inner.WithWhere(query.Equal(aliased.Column("user_id"), users.Column("id")))
	require.NoError(t, err)
	outer, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	outer, err = outer.WithWhere(query.Exists(inner))
	require.NoError(t, err)
	_, err = render.Select(dialect.SQLite(), outer)
	require.ErrorContains(t, err, "sqlite")
	require.ErrorContains(t, err, "USERS")
	require.ErrorContains(t, err, "users")
	require.ErrorContains(t, err, "distinct alias")

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	deleteStatement, err := query.NewDelete(users)
	require.NoError(t, err)
	deleteStatement, err = deleteStatement.WithWhere(query.Exists(inner))
	require.NoError(t, err)
	result, err := rasql.Exec(t.Context(), db, deleteStatement)
	require.ErrorContains(t, err, "distinct alias")
	require.Nil(t, result)
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT count(*) FROM users").Scan(&count))
	require.Equal(t, 2, count)

	distinct, err := orders.As("o")
	require.NoError(t, err)
	inner, err = query.NewSelect(distinct, query.Project(query.Bind(1)))
	require.NoError(t, err)
	inner, err = inner.WithCorrelation(users)
	require.NoError(t, err)
	inner, err = inner.WithWhere(query.Equal(distinct.Column("user_id"), users.Column("id")))
	require.NoError(t, err)
	outer, err = query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	outer, err = outer.WithWhere(query.Exists(inner))
	require.NoError(t, err)
	rendered, err := render.Select(dialect.SQLite(), outer)
	require.NoError(t, err)
	rows, err := database.QueryContext(t.Context(), rendered.SQL(), rendered.Args()...)
	require.NoError(t, err)
	defer rows.Close()
	require.True(t, rows.Next())
	var id int64
	require.NoError(t, rows.Scan(&id))
	require.Equal(t, int64(1), id)
	require.False(t, rows.Next())
	require.NoError(t, rows.Err())
}
