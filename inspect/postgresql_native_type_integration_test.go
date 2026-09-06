//go:build unix

package inspect_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestNativeTypePostgreSQL(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)
	enumName := dbtest.UniqueName(t, "rasql_mood")
	domainName := dbtest.UniqueName(t, "rasql_amount")
	tableName := dbtest.UniqueName(t, "rasql_native")
	quoted := func(value string) string { return `"` + value + `"` }
	mustExec(t, ctx, database, "CREATE TYPE "+quoted(enumName)+" AS ENUM ('sad', 'happy', 'needs,comma', 'quote''s', '')")
	mustExec(t, ctx, database, "CREATE DOMAIN "+quoted(domainName)+" AS NUMERIC(12,3)")
	mustExec(t, ctx, database, "CREATE TABLE "+quoted(tableName)+" (mood "+quoted(enumName)+", moods "+quoted(enumName)+"[], amount "+quoted(domainName)+", arbitrary NUMERIC, payload JSON, payload_binary JSONB, happened TIMESTAMP(3) WITH TIME ZONE, local_time TIME WITHOUT TIME ZONE, day DATE)")

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
	require.Equal(t, "time", byName["local_time"].NativeType.Name)
	require.Equal(t, "date", byName["day"].NativeType.Name)
}
