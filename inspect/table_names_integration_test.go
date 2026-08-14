//go:build unix

package inspect_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestPostgreSQLInspectorReadsTableNamesAgainstLiveDatabase confirms
// TableNames' pg_catalog query against a real server, not only the sqlmock
// fixture in inspect_test.go: that ordinary tables in current_schema() come
// back sorted, and that a view of the same name pattern is excluded.
func TestPostgreSQLInspectorReadsTableNamesAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE zebras (id integer PRIMARY KEY)")
	mustExec(t, ctx, database, "CREATE TABLE armadillos (id integer PRIMARY KEY)")
	mustExec(t, ctx, database, "CREATE VIEW zebra_view AS SELECT id FROM zebras")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	names, err := inspector.TableNames(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"armadillos", "zebras"}, names)
}

// TestMySQLInspectorReadsTableNamesAgainstLiveDatabase is the MySQL
// counterpart: information_schema.tables filtered to table_type =
// 'BASE TABLE' must exclude a view scoped to the same DATABASE().
func TestMySQLInspectorReadsTableNamesAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.MySQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE zebras (id integer PRIMARY KEY)")
	mustExec(t, ctx, database, "CREATE TABLE armadillos (id integer PRIMARY KEY)")
	mustExec(t, ctx, database, "CREATE VIEW zebra_view AS SELECT id FROM zebras")

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	names, err := inspector.TableNames(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"armadillos", "zebras"}, names)
}
