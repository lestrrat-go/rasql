//go:build unix

package inspect_test

import (
	"context"
	"database/sql"
	"strconv"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// postgreSQLLiveServerVersion reads the live server's server_version_num,
// the same integer form inspect.go itself parses (see
// postgreSQLServerVersion), so this test can decide whether the connected
// server is new enough to support a VIRTUAL generated column (PostgreSQL
// 18) without guessing from compose.yaml's pinned image tag.
func postgreSQLLiveServerVersion(t *testing.T, ctx context.Context, database *sql.DB) int {
	t.Helper()
	var value string
	require.NoError(t, database.QueryRowContext(ctx, "SHOW server_version_num").Scan(&value), "read live PostgreSQL server_version_num")
	version, err := strconv.Atoi(value)
	require.NoError(t, err, "parse live PostgreSQL server_version_num %q", value)
	return version
}

// TestPostgreSQLInspectorRecordsGeneratedColumnAgainstLiveDatabase pins what
// a real PostgreSQL server reports back for a generated column, because
// TestPostgreSQLInspectorRecordsGeneratedColumns in inspect_test.go only
// asserts rasql's own mocked catalog echoes what the test told it to.
// Before this feature existed, the PostgreSQL columns query selected no
// generated-column metadata at all, so a generated column inspected
// silently as an ordinary column with an empty default instead of being
// flagged as generated -- the exact silent mislabeling this test proves no
// longer happens against a real server.
//
// A VIRTUAL generated column needs PostgreSQL 18 or later; compose.yaml
// pins postgres:17-alpine (see compose.yaml), so that half of the test
// reads the live server's own version and skips cleanly, rather than
// asserting something this server cannot produce, when it predates 18.
func TestPostgreSQLInspectorRecordsGeneratedColumnAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE live_generated_stored (id BIGINT PRIMARY KEY, celsius BIGINT NOT NULL, fahrenheit BIGINT GENERATED ALWAYS AS (celsius * 9 / 5 + 32) STORED)")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "live_generated_stored")
	require.NoError(t, err)

	fahrenheit, ok := columnNamed(table.Columns, "fahrenheit")
	require.True(t, ok, "inspected table has a fahrenheit column")
	require.Equal(t, "celsius * 9 / 5 + 32", fahrenheit.GeneratedExpression,
		"the live catalog's generation_expression must round-trip verbatim")
	require.Equal(t, schema.GeneratedStored, fahrenheit.GeneratedStorage,
		"pg_catalog.pg_attribute.attgenerated must report a STORED column as such")
	require.Empty(t, fahrenheit.Default,
		"a generated column carries no separate Default; PostgreSQL never lets the two coexist")

	version := postgreSQLLiveServerVersion(t, ctx, database)
	if version < 180000 {
		t.Logf("skipping VIRTUAL generated column assertion: live PostgreSQL server reports version %d, VIRTUAL generated columns need PostgreSQL 18 or later", version)
		return
	}

	mustExec(t, ctx, database, "CREATE TABLE live_generated_virtual (id BIGINT PRIMARY KEY, celsius BIGINT NOT NULL, fahrenheit BIGINT GENERATED ALWAYS AS (celsius * 9 / 5 + 32) VIRTUAL)")
	virtualTable, err := inspector.Table(ctx, "live_generated_virtual")
	require.NoError(t, err)

	virtualFahrenheit, ok := columnNamed(virtualTable.Columns, "fahrenheit")
	require.True(t, ok, "inspected virtual table has a fahrenheit column")
	require.Equal(t, "celsius * 9 / 5 + 32", virtualFahrenheit.GeneratedExpression)
	require.Equal(t, schema.GeneratedVirtual, virtualFahrenheit.GeneratedStorage,
		"pg_catalog.pg_attribute.attgenerated must report a VIRTUAL column as such on PostgreSQL 18+")
}

// TestMySQLInspectorRecordsGeneratedColumnsAgainstLiveDatabase pins what a
// real MySQL server reports back for both generated column storage kinds,
// because TestMySQLInspectorRecordsGeneratedColumns in inspect_test.go only
// asserts rasql's own mocked catalog echoes what the test told it to.
// Before this feature existed, MySQL inspection selected no
// generated-column metadata at all, so a generated column round-tripped
// indistinguishably from an ordinary, writable one -- the exact gap this
// test proves no longer exists against a real server.
func TestMySQLInspectorRecordsGeneratedColumnsAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.MySQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE live_generated_measurements ("+
		"id BIGINT PRIMARY KEY, "+
		"celsius BIGINT NOT NULL, "+
		"fahrenheit_stored BIGINT GENERATED ALWAYS AS (celsius * 9 / 5 + 32) STORED, "+
		"fahrenheit_virtual BIGINT GENERATED ALWAYS AS (celsius * 9 / 5 + 32) VIRTUAL"+
		") ENGINE=InnoDB")

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "live_generated_measurements")
	require.NoError(t, err)

	stored, ok := columnNamed(table.Columns, "fahrenheit_stored")
	require.True(t, ok, "inspected table has a fahrenheit_stored column")
	require.Equal(t, "celsius * 9 / 5 + 32", stored.GeneratedExpression,
		"the live catalog's GENERATION_EXPRESSION must round-trip verbatim")
	require.Equal(t, schema.GeneratedStored, stored.GeneratedStorage,
		"EXTRA reporting \"STORED GENERATED\" must map to schema.GeneratedStored")

	virtual, ok := columnNamed(table.Columns, "fahrenheit_virtual")
	require.True(t, ok, "inspected table has a fahrenheit_virtual column")
	require.Equal(t, "celsius * 9 / 5 + 32", virtual.GeneratedExpression)
	require.Equal(t, schema.GeneratedVirtual, virtual.GeneratedStorage,
		"EXTRA reporting \"VIRTUAL GENERATED\" must map to schema.GeneratedVirtual")
}

// columnNamed returns the column named name from columns, and whether one
// was found.
func columnNamed(columns []schema.ColumnDef, name string) (schema.ColumnDef, bool) {
	for _, candidate := range columns {
		if candidate.Name == name {
			return candidate, true
		}
	}
	return schema.ColumnDef{}, false
}
