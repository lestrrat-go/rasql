//go:build unix

package rasqlgen

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
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

// bootstrapLiveRefreshModule sets up what a live refresh test needs beyond
// bootstrapLiveSweep's own -dsn override: a real Go module bootstrap can
// write into and, later, a refresh can `go list`/`go run` against, since
// loading an existing description package's Tables() runs a child program
// the same way `rasqlgen schema -source` does. The module carries a
// replace directive back to this checkout and no go.sum, exactly like
// newBootstrapRoundTripModule in bootstrap_roundtrip_test.go; that helper
// lives in the external rasqlgen_test package and cannot be reused here,
// where openDatabase's override requires this package's own internal
// visibility.
func bootstrapLiveRefreshModule(t *testing.T, database *sql.DB) string {
	t.Helper()
	previousOpenDatabase := openDatabase
	openDatabase = func(driverName, dsn string) (*sql.DB, error) { return database, nil }
	t.Cleanup(func() { openDatabase = previousOpenDatabase })

	moduleDir := t.TempDir()
	repoGoMod, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	require.NoError(t, err)
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	module := strings.Replace(string(repoGoMod), "module github.com/lestrrat-go/rasql\n", "module example.com/liverefresh\n", 1)
	module += "\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte(module), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(moduleDir, "internal", "tables"), 0o700))

	t.Chdir(moduleDir)
	return moduleDir
}

// runBootstrapCapturingOutput runs `rasqlgen bootstrap` with args and
// returns its command output, the same way TestRunHelp captures it: by
// swapping the package-level commandOutput for the duration of the call.
func runBootstrapCapturingOutput(t *testing.T, args []string) string {
	t.Helper()
	previousCommandOutput := commandOutput
	output := new(bytes.Buffer)
	commandOutput = output
	t.Cleanup(func() { commandOutput = previousCommandOutput })

	require.NoError(t, run(args), output.String())
	return output.String()
}

// TestRunBootstrapRefreshLivePostgreSQLDatabase confirms a refresh against
// a real PostgreSQL server: a table added and a table dropped both show up
// in a report-only run's output, and -write applies exactly that, deleting
// the dropped table's file and dropping it from the aggregator.
func TestRunBootstrapRefreshLivePostgreSQLDatabase(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	ctx := t.Context()
	mustExecLive(t, ctx, database, "CREATE TABLE users (id integer PRIMARY KEY, email text NOT NULL)")
	mustExecLive(t, ctx, database, "CREATE TABLE legacy_users (id integer PRIMARY KEY)")

	moduleDir := bootstrapLiveRefreshModule(t, database)
	require.NoError(t, run([]string{"bootstrap", "-dsn", "placeholder", "-dialect", "postgresql", "-package", "tables", "-output", "internal/tables"}))
	outputDir := filepath.Join(moduleDir, "internal", "tables")
	require.FileExists(t, filepath.Join(outputDir, "legacy_users_gen.go"))

	mustExecLive(t, ctx, database, "CREATE TABLE orders (id integer PRIMARY KEY, user_id integer NOT NULL REFERENCES users (id))")
	mustExecLive(t, ctx, database, "DROP TABLE legacy_users")

	report := runBootstrapCapturingOutput(t, []string{"bootstrap", "-dsn", "placeholder", "-dialect", "postgresql", "-package", "tables", "-output", "internal/tables"})
	require.Contains(t, report, `+ table "orders"`)
	require.Contains(t, report, `- table "legacy_users"`)
	require.FileExists(t, filepath.Join(outputDir, "legacy_users_gen.go"), "a report-only refresh must not delete anything")

	require.NoError(t, run([]string{"bootstrap", "-dsn", "placeholder", "-dialect", "postgresql", "-package", "tables", "-output", "internal/tables", "-write"}))
	require.FileExists(t, filepath.Join(outputDir, "orders_gen.go"))
	require.NoFileExists(t, filepath.Join(outputDir, "legacy_users_gen.go"))
	aggregator, err := os.ReadFile(filepath.Join(outputDir, "tables_gen.go"))
	require.NoError(t, err)
	require.NotContains(t, string(aggregator), "LegacyUsersDef")
}

// TestRunBootstrapRefreshLiveMySQLDatabase is
// TestRunBootstrapRefreshLivePostgreSQLDatabase's MySQL counterpart.
func TestRunBootstrapRefreshLiveMySQLDatabase(t *testing.T) {
	database := dbtest.MySQLDB(t)
	ctx := t.Context()
	mustExecLive(t, ctx, database, "CREATE TABLE users (id integer PRIMARY KEY, email varchar(255) NOT NULL)")
	mustExecLive(t, ctx, database, "CREATE TABLE legacy_users (id integer PRIMARY KEY)")

	moduleDir := bootstrapLiveRefreshModule(t, database)
	require.NoError(t, run([]string{"bootstrap", "-dsn", "placeholder", "-dialect", "mysql", "-package", "tables", "-output", "internal/tables"}))
	outputDir := filepath.Join(moduleDir, "internal", "tables")
	require.FileExists(t, filepath.Join(outputDir, "legacy_users_gen.go"))

	mustExecLive(t, ctx, database, "CREATE TABLE orders (id integer PRIMARY KEY, user_id integer NOT NULL, FOREIGN KEY (user_id) REFERENCES users (id))")
	mustExecLive(t, ctx, database, "DROP TABLE legacy_users")

	report := runBootstrapCapturingOutput(t, []string{"bootstrap", "-dsn", "placeholder", "-dialect", "mysql", "-package", "tables", "-output", "internal/tables"})
	require.Contains(t, report, `+ table "orders"`)
	require.Contains(t, report, `- table "legacy_users"`)
	require.FileExists(t, filepath.Join(outputDir, "legacy_users_gen.go"), "a report-only refresh must not delete anything")

	require.NoError(t, run([]string{"bootstrap", "-dsn", "placeholder", "-dialect", "mysql", "-package", "tables", "-output", "internal/tables", "-write"}))
	require.FileExists(t, filepath.Join(outputDir, "orders_gen.go"))
	require.NoFileExists(t, filepath.Join(outputDir, "legacy_users_gen.go"))
	aggregator, err := os.ReadFile(filepath.Join(outputDir, "tables_gen.go"))
	require.NoError(t, err)
	require.NotContains(t, string(aggregator), "LegacyUsersDef")
}
