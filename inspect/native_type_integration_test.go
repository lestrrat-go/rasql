//go:build unix

package inspect_test

import (
	"database/sql"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
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
	var columnType, setType string
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT column_type FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = 'mood'", tableName).Scan(&columnType))
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT column_type FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = 'flags'", tableName).Scan(&setType))
	require.Equal(t, "enum('needs,comma','quote''s','  spaced','back\\\\slash','')", columnType)
	require.Equal(t, "set('one','two')", setType)
	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), tableName)
	require.NoError(t, err)
	require.Equal(t, []string{"needs,comma", "quote's", "  spaced", "back\\slash", ""}, table.Columns[0].NativeType.Arguments)
	require.Equal(t, []string{"one", "two"}, table.Columns[1].NativeType.Arguments)
	catalogTables, err := catalog.FromQueryer(t.Context(), database, catalog.Options{Dialect: dialect.MySQL(), Include: []string{tableName}})
	require.NoError(t, err)
	require.Equal(t, table, catalogTables[0])
	descriptor, err := generate.DescriptorSource("nativefixture", table)
	require.NoError(t, err)
	_, err = parser.ParseFile(token.NewFileSet(), "descriptor.go", descriptor, parser.AllErrors)
	require.NoError(t, err)
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
	_, err = first.ExecContext(t.Context(), `CREATE TABLE source (a VARCHAR(12) DEFAULT 'x', b INT, c WEIRD_TYPE, d CHAR(4), e FLOAT, f DOUBLE PRECISION, g BOOLEAN, h JSON, i DATE, j TIME, k BLOB DEFAULT X'01', q "Custom Type", r "A""B")`)
	require.NoError(t, err)
	inspector, err := inspect.New(first, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "source")
	require.NoError(t, err)
	require.Equal(t, &schema.NativeTypeDef{Dialect: "sqlite", Name: "VARCHAR", Kind: schema.NativeOther, Arguments: []string{"12"}}, table.Columns[0].NativeType)
	require.Equal(t, &schema.NativeTypeDef{Dialect: "sqlite", Name: "INT", Kind: schema.NativeOther}, table.Columns[1].NativeType)
	require.Equal(t, &schema.NativeTypeDef{Dialect: "sqlite", Name: "WEIRD_TYPE", Kind: schema.NativeOther}, table.Columns[2].NativeType)
	require.Equal(t, &schema.NativeTypeDef{Dialect: "sqlite", Name: "CHAR", Kind: schema.NativeOther, Arguments: []string{"4"}}, table.Columns[3].NativeType)
	require.Equal(t, "FLOAT", table.Columns[4].NativeType.Name)
	require.Equal(t, "DOUBLE PRECISION", table.Columns[5].NativeType.Name)
	require.NotNil(t, table.Columns[6].NativeType)
	require.NotNil(t, table.Columns[7].NativeType)
	require.NotNil(t, table.Columns[8].NativeType)
	require.NotNil(t, table.Columns[9].NativeType)
	require.Equal(t, "BOOLEAN", table.Columns[6].NativeType.Name)
	require.Equal(t, "JSON", table.Columns[7].NativeType.Name)
	require.Equal(t, "DATE", table.Columns[8].NativeType.Name)
	require.Equal(t, "TIME", table.Columns[9].NativeType.Name)
	require.Equal(t, &schema.NativeTypeDef{Dialect: "sqlite", Name: "Custom Type", Kind: schema.NativeOther}, table.Columns[11].NativeType)
	require.Equal(t, &schema.NativeTypeDef{Dialect: "sqlite", Name: `A"B`, Kind: schema.NativeOther}, table.Columns[12].NativeType)
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
