//go:build unix

package rasqlgen

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// schemaLiveSweep is schema's counterpart to bootstrapLiveSweep in
// bootstrap_integration_test.go: it drives `schema -dsn`'s own -dsn code
// path against an already-open, already-connected database, the one way a
// live test can reach it, since dbtest never hands back a raw DSN string.
func schemaLiveSweep(t *testing.T, database *sql.DB, dialectName string) string {
	t.Helper()
	previousOpenDatabase := openDatabase
	openDatabase = func(driverName, dsn string) (*sql.DB, error) { return database, nil }
	t.Cleanup(func() { openDatabase = previousOpenDatabase })

	directory := t.TempDir()
	require.NoError(t, run([]string{"schema", "-dsn", "placeholder", "-dialect", dialectName, "-package", "store", "-output", directory}))
	return directory
}

// TestRunSchemaSweepsLivePostgreSQLDatabase is
// TestRunBootstrapSweepsLivePostgreSQLDatabase's `schema -dsn` counterpart:
// it confirms a sweep against a real PostgreSQL server, not sqlmock,
// describes an ordinary table and skips the default migration history
// table, now that `schema -dsn` sweeps by default instead of requiring
// -table.
func TestRunSchemaSweepsLivePostgreSQLDatabase(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	ctx := t.Context()
	mustExecLive(t, ctx, database, "CREATE TABLE users (id integer PRIMARY KEY, email text NOT NULL, active boolean NOT NULL DEFAULT true, created_at timestamp NOT NULL)")
	mustExecLive(t, ctx, database, "CREATE TABLE rasql_schema_migrations (id text PRIMARY KEY)")
	mustExecLive(t, ctx, database, "CREATE VIEW active_users AS SELECT id FROM users WHERE active")

	directory := schemaLiveSweep(t, database, "postgresql")

	require.FileExists(t, filepath.Join(directory, "users_gen.go"))
	require.FileExists(t, filepath.Join(directory, "schema_gen.go"))
	require.NoFileExists(t, filepath.Join(directory, "rasql_schema_migrations_gen.go"))
	require.NoFileExists(t, filepath.Join(directory, "active_users_gen.go"))
}

// TestRunSchemaSweepsLiveMySQLDatabase is
// TestRunSchemaSweepsLivePostgreSQLDatabase's MySQL counterpart.
func TestRunSchemaSweepsLiveMySQLDatabase(t *testing.T) {
	database := dbtest.MySQLDB(t)
	ctx := t.Context()
	mustExecLive(t, ctx, database, "CREATE TABLE users (id integer PRIMARY KEY, email varchar(255) NOT NULL, active boolean NOT NULL DEFAULT true, created_at timestamp NOT NULL)")
	mustExecLive(t, ctx, database, "CREATE TABLE rasql_schema_migrations (id varchar(255) PRIMARY KEY)")
	mustExecLive(t, ctx, database, "CREATE VIEW active_users AS SELECT id FROM users WHERE active")

	directory := schemaLiveSweep(t, database, "mysql")

	require.FileExists(t, filepath.Join(directory, "users_gen.go"))
	require.FileExists(t, filepath.Join(directory, "schema_gen.go"))
	require.NoFileExists(t, filepath.Join(directory, "rasql_schema_migrations_gen.go"))
	require.NoFileExists(t, filepath.Join(directory, "active_users_gen.go"))
}
