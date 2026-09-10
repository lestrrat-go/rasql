package render_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type autoincrementRow struct{}

func TestSQLiteAutoincrementCreateTableAndNoReuse(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := rasql.TableOf[autoincrementRow](schema.TableDef{
		Name:       "items",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "status", Type: schema.TextType{}, Default: "'pending'"}},
		PrimaryKey: []string{"id"}, PrimaryKeyAutoincrement: true,
	})
	require.NoError(t, err)
	require.NoError(t, rasql.CreateTable(t.Context(), db, table))
	_, err = database.ExecContext(t.Context(), "INSERT INTO items DEFAULT VALUES")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "INSERT INTO items DEFAULT VALUES")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "DELETE FROM items WHERE id = 2")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "INSERT INTO items DEFAULT VALUES")
	require.NoError(t, err)
	var ids []int
	rows, err := database.QueryContext(t.Context(), "SELECT id FROM items ORDER BY id")
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []int{1, 3}, ids)
	var ddl string
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT sql FROM sqlite_master WHERE name = 'items'").Scan(&ddl))
	require.Contains(t, ddl, "PRIMARY KEY AUTOINCREMENT")
}
