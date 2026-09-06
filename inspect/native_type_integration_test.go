//go:build unix

package inspect_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestNativeTypeMySQL(t *testing.T) {
	database := dbtest.MySQLDB(t)
	tableName := dbtest.UniqueName(t, "rasql_native")
	statement := "CREATE TABLE `" + tableName + "` (`mood` ENUM('needs,comma','quote''s','  spaced  ','back\\\\slash',''), `flags` SET('one','two'))"
	_, err := database.ExecContext(t.Context(), statement)
	require.NoError(t, err)
	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), tableName)
	require.NoError(t, err)
	require.Equal(t, []string{"needs,comma", "quote's", "  spaced", "back\\slash", ""}, table.Columns[0].NativeType.Arguments)
	require.Equal(t, []string{"one", "two"}, table.Columns[1].NativeType.Arguments)
	table.Name = dbtest.UniqueName(t, "rasql_native_copy")
	rendered, err := render.CreateTable(dialect.MySQL(), table)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), rendered.SQL())
	require.NoError(t, err)
	roundTrip, err := inspector.Table(t.Context(), table.Name)
	require.NoError(t, err)
	require.Equal(t, table.Columns, roundTrip.Columns)
}

func TestNativeTypeSQLiteRoundTrip(t *testing.T) {
	first, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "first.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })
	_, err = first.ExecContext(t.Context(), `CREATE TABLE source (a VARCHAR(12) DEFAULT 'x', b INT, c WEIRD_TYPE)`)
	require.NoError(t, err)
	inspector, err := inspect.New(first, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "source")
	require.NoError(t, err)
	require.Equal(t, &schema.NativeTypeDef{Dialect: "sqlite", Name: "VARCHAR(12)", Kind: schema.NativeOther}, table.Columns[0].NativeType)
	require.Equal(t, &schema.NativeTypeDef{Dialect: "sqlite", Name: "INT", Kind: schema.NativeOther}, table.Columns[1].NativeType)
	require.Equal(t, schema.OpaqueType{}, table.Columns[2].Type)
	rendered, err := render.CreateTable(dialect.SQLite(), table)
	require.NoError(t, err)
	second, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "second.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })
	_, err = second.ExecContext(t.Context(), rendered.SQL())
	require.NoError(t, err)
	secondInspector, err := inspect.New(second, dialect.SQLite())
	require.NoError(t, err)
	roundTrip, err := secondInspector.Table(t.Context(), "source")
	require.NoError(t, err)
	require.Equal(t, table.Columns, roundTrip.Columns)
}
