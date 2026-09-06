//go:build unix

package inspect_test

import (
	"database/sql"
	"go/parser"
	"go/token"
	"testing"
	"time"

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
	insertedAt := time.Date(2026, time.January, 2, 3, 4, 5, 123000000, time.UTC)
	_, err = database.ExecContext(ctx, "INSERT INTO "+quoted(tableName)+" (mood, moods, amount, arbitrary, payload, payload_binary, happened, happened_plain, zoned_time, local_time, day, pair) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12), (NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)",
		nativeTextValue{Text: "quote's", Valid: true}, nativeTextValue{Text: "{sad,happy}", Valid: true}, nativeTextValue{Text: "12.345", Valid: true}, nativeTextValue{Text: "987.65", Valid: true},
		nativeTextValue{Text: `{"k":1}`, Valid: true}, nativeTextValue{Text: `{"raw":true}`, Valid: true}, insertedAt, insertedAt, nativeTextValue{Text: "03:04:05+00", Valid: true}, nativeTextValue{Text: "03:04:05", Valid: true},
		nativeTextValue{Text: "2026-01-02", Valid: true}, nativeTextValue{Text: "(7)", Valid: true})
	require.NoError(t, err)
	var mood, moods, amount, arbitrary, payload, payloadBinary, happened, happenedPlain, zonedTime, localTime, day, pair nativeTextValue
	require.NoError(t, database.QueryRowContext(ctx, "SELECT mood, moods, amount, arbitrary, payload, payload_binary, happened, happened_plain, zoned_time, local_time, day, pair FROM "+quoted(tableName)+" WHERE mood IS NOT NULL").Scan(&mood, &moods, &amount, &arbitrary, &payload, &payloadBinary, &happened, &happenedPlain, &zonedTime, &localTime, &day, &pair))
	for _, value := range []*nativeTextValue{&mood, &moods, &amount, &arbitrary, &payload, &payloadBinary, &happened, &happenedPlain, &zonedTime, &localTime, &day, &pair} {
		require.True(t, value.Valid)
	}
	require.Equal(t, "quote's", mood.Text)
	require.Equal(t, "{sad,happy}", moods.Text)
	require.Equal(t, "12.345", amount.Text)
	require.Equal(t, `{"k":1}`, payload.Text)
	require.Equal(t, `{"raw":true}`, payloadBinary.Text)
	var nullValues [12]nativeTextValue
	require.NoError(t, database.QueryRowContext(ctx, "SELECT mood, moods, amount, arbitrary, payload, payload_binary, happened, happened_plain, zoned_time, local_time, day, pair FROM "+quoted(tableName)+" WHERE mood IS NULL").Scan(&nullValues[0], &nullValues[1], &nullValues[2], &nullValues[3], &nullValues[4], &nullValues[5], &nullValues[6], &nullValues[7], &nullValues[8], &nullValues[9], &nullValues[10], &nullValues[11]))
	for _, value := range nullValues {
		require.False(t, value.Valid)
	}
	var _ sql.Scanner = (*nativeTextValue)(nil)

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
