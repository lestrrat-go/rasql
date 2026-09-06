//go:build unix

package inspect_test

import (
	"go/parser"
	"go/token"
	"testing"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestNativeTypePostgreSQL(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)
	enumName := dbtest.UniqueName(t, "rasql_mood")
	domainName := dbtest.UniqueName(t, "rasql_amount")
	compositeName := dbtest.UniqueName(t, "rasql_pair")
	tableName := dbtest.UniqueName(t, "rasql_native")
	renderedName := dbtest.UniqueName(t, "rasql_native_copy")
	quoted := func(value string) string { return `"` + value + `"` }
	mustExec(t, ctx, database, "CREATE TYPE "+quoted(enumName)+" AS ENUM ('sad', 'happy', 'needs,comma', 'quote''s', '')")
	mustExec(t, ctx, database, "CREATE DOMAIN "+quoted(domainName)+" AS NUMERIC(12,3)")
	mustExec(t, ctx, database, "CREATE TYPE "+quoted(compositeName)+" AS (value text)")
	mustExec(t, ctx, database, "CREATE TABLE "+quoted(tableName)+" (mood "+quoted(enumName)+", moods "+quoted(enumName)+"[], amount "+quoted(domainName)+", arbitrary NUMERIC, payload JSON, payload_binary JSONB, happened TIMESTAMP(3) WITH TIME ZONE, happened_plain TIMESTAMP WITHOUT TIME ZONE, zoned_time TIME(0) WITH TIME ZONE, local_time TIME WITHOUT TIME ZONE, day DATE, pair "+quoted(compositeName)+")")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, tableName)
	require.NoError(t, err)
	byName := make(map[string]schema.ColumnDef, len(table.Columns))
	for _, column := range table.Columns {
		byName[column.Name] = column
	}
	require.Equal(t, &schema.NativeTypeDef{Dialect: "postgresql", Schema: "public", Name: enumName, Kind: schema.NativeEnum, Arguments: []string{"sad", "happy", "needs,comma", "quote's", ""}}, byName["mood"].NativeType)
	require.Equal(t, schema.OpaqueType{}, byName["moods"].Type)
	require.Equal(t, schema.NativeArray, byName["moods"].NativeType.Kind)
	require.Equal(t, enumName, byName["moods"].NativeType.Element.Name)
	require.Equal(t, schema.DecimalType{Precision: 12, Scale: schema.NewDecimalScale(3)}, byName["amount"].Type)
	require.Equal(t, domainName, byName["amount"].NativeType.Name)
	require.Equal(t, schema.OpaqueType{}, byName["arbitrary"].Type)
	require.Equal(t, "numeric", byName["arbitrary"].NativeType.Name)
	require.Equal(t, "json", byName["payload"].NativeType.Name)
	require.Equal(t, "jsonb", byName["payload_binary"].NativeType.Name)
	require.Equal(t, schema.TimeType{}, byName["happened"].Type)
	require.Equal(t, "timestamptz", byName["happened"].NativeType.Name)
	require.Equal(t, []string{"3"}, byName["happened"].NativeType.Arguments)
	require.Equal(t, "timestamp", byName["happened_plain"].NativeType.Name)
	require.Equal(t, []string{"6"}, byName["happened_plain"].NativeType.Arguments)
	require.Equal(t, "timetz", byName["zoned_time"].NativeType.Name)
	require.Equal(t, []string{"0"}, byName["zoned_time"].NativeType.Arguments)
	require.Equal(t, "time", byName["local_time"].NativeType.Name)
	require.Equal(t, "date", byName["day"].NativeType.Name)
	require.Equal(t, schema.NativeOther, byName["pair"].NativeType.Kind)

	catalogTables, err := catalog.FromQueryer(ctx, database, catalog.Options{Dialect: dialect.PostgreSQL(), Include: []string{tableName}})
	require.NoError(t, err)
	require.Len(t, catalogTables, 1)
	require.Equal(t, table, catalogTables[0])
	descriptor, err := generate.DescriptorSource("nativefixture", table)
	require.NoError(t, err)
	_, err = parser.ParseFile(token.NewFileSet(), "descriptor.go", descriptor, parser.AllErrors)
	require.NoError(t, err)
	require.Contains(t, string(descriptor), enumName)
	require.Contains(t, string(descriptor), "timestamptz")

	copyTable := table.Clone()
	copyTable.Name = renderedName
	statement, err := render.CreateTable(dialect.PostgreSQL(), copyTable)
	require.NoError(t, err)
	mustExec(t, ctx, database, statement.SQL())
	inspectedCopy, err := inspector.Table(ctx, renderedName)
	require.NoError(t, err)
	copyTable.Name = renderedName
	require.Equal(t, copyTable.Columns, inspectedCopy.Columns)
}
