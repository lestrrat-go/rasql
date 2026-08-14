//go:build unix

package rasqlgen

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func mustExecLive(t *testing.T, ctx context.Context, db *sql.DB, statement string) {
	t.Helper()
	_, err := db.ExecContext(ctx, statement)
	require.NoError(t, err)
}

// bootstrapLiveSweep runs a bootstrap sweep against database, which
// openDatabase is overridden to hand back regardless of the placeholder DSN
// passed on the command line: dbtest never hands back a raw DSN string, only
// an already-open, already-connected *sql.DB, so this is the one way a live
// test can drive the command's own -dsn code path with dbtest's connection.
func bootstrapLiveSweep(t *testing.T, database *sql.DB, dialectName string) string {
	t.Helper()
	previousOpenDatabase := openDatabase
	openDatabase = func(driverName, dsn string) (*sql.DB, error) { return database, nil }
	t.Cleanup(func() { openDatabase = previousOpenDatabase })

	directory := t.TempDir()
	require.NoError(t, run([]string{"bootstrap", "-dsn", "placeholder", "-dialect", dialectName, "-package", "schemasource", "-output", directory}))
	return directory
}

// TestRunBootstrapSweepsLivePostgreSQLDatabase confirms a sweep against a
// real PostgreSQL server, not sqlmock, describes an ordinary table and
// skips the default migration history table.
//
// The fixture keeps to integer, text, boolean and timestamp columns
// deliberately: rasql's own PostgreSQL type mapping does not yet cover
// every column type PostgreSQL allows (an array column, for one, fails
// inspection outright), and a sweep test exists to prove the common path
// works end to end, not to catalog every unsupported type.
func TestRunBootstrapSweepsLivePostgreSQLDatabase(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	ctx := t.Context()
	mustExecLive(t, ctx, database, "CREATE TABLE users (id integer PRIMARY KEY, email text NOT NULL, active boolean NOT NULL DEFAULT true, created_at timestamp NOT NULL)")
	mustExecLive(t, ctx, database, "CREATE TABLE rasql_schema_migrations (id text PRIMARY KEY)")
	mustExecLive(t, ctx, database, "CREATE VIEW active_users AS SELECT id FROM users WHERE active")

	directory := bootstrapLiveSweep(t, database, "postgresql")

	require.FileExists(t, filepath.Join(directory, "users_gen.go"))
	require.FileExists(t, filepath.Join(directory, "tables_gen.go"))
	require.NoFileExists(t, filepath.Join(directory, "rasql_schema_migrations_gen.go"))
	require.NoFileExists(t, filepath.Join(directory, "active_users_gen.go"))
}

// TestRunBootstrapSweepsLiveMySQLDatabase is TestRunBootstrapSweepsLivePostgreSQLDatabase's
// MySQL counterpart.
func TestRunBootstrapSweepsLiveMySQLDatabase(t *testing.T) {
	database := dbtest.MySQLDB(t)
	ctx := t.Context()
	mustExecLive(t, ctx, database, "CREATE TABLE users (id integer PRIMARY KEY, email varchar(255) NOT NULL, active boolean NOT NULL DEFAULT true, created_at timestamp NOT NULL)")
	mustExecLive(t, ctx, database, "CREATE TABLE rasql_schema_migrations (id varchar(255) PRIMARY KEY)")
	mustExecLive(t, ctx, database, "CREATE VIEW active_users AS SELECT id FROM users WHERE active")

	directory := bootstrapLiveSweep(t, database, "mysql")

	require.FileExists(t, filepath.Join(directory, "users_gen.go"))
	require.FileExists(t, filepath.Join(directory, "tables_gen.go"))
	require.NoFileExists(t, filepath.Join(directory, "rasql_schema_migrations_gen.go"))
	require.NoFileExists(t, filepath.Join(directory, "active_users_gen.go"))
}
