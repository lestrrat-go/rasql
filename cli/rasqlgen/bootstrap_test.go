package rasqlgen

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func mustCreateSQLite(t *testing.T, statements ...string) string {
	t.Helper()
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "schema.db")
	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	for _, statement := range statements {
		_, err = database.ExecContext(t.Context(), statement)
		require.NoError(t, err)
	}
	require.NoError(t, database.Close())
	return databasePath
}

func TestRunBootstrapRequiresDSN(t *testing.T) {
	directory := t.TempDir()
	err := run([]string{"bootstrap", "-package", "generated", "-output", directory})
	require.ErrorContains(t, err, "bootstrap requires -dsn")
}

func TestRunBootstrapRequiresPackageAndOutput(t *testing.T) {
	err := run([]string{"bootstrap", "-dsn", "schema.db", "-dialect", "sqlite"})
	require.ErrorContains(t, err, "bootstrap requires -package and -output")
}

func TestRunBootstrapRejectsNonPositiveTimeout(t *testing.T) {
	directory := t.TempDir()
	err := run([]string{"bootstrap", "-dsn", "schema.db", "-package", "generated", "-output", directory, "-timeout", "0s"})
	require.ErrorContains(t, err, "bootstrap -timeout must be positive")
}

func TestRunBootstrapRejectsTableAndExcludeTogether(t *testing.T) {
	directory := t.TempDir()
	err := run([]string{"bootstrap", "-dsn", "schema.db", "-package", "generated", "-output", directory, "-table", "users", "-exclude", "orders"})
	require.ErrorContains(t, err, "bootstrap accepts -table or -exclude, not both")
}

// TestRunBootstrapSweepsSQLite exercises the default sweep: every base
// table is described, and the migration history table is skipped by
// default.
func TestRunBootstrapSweepsSQLite(t *testing.T) {
	databasePath := mustCreateSQLite(t,
		"CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)",
		"CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users (id))",
		"CREATE TABLE rasql_schema_migrations (id TEXT PRIMARY KEY)",
		"CREATE VIEW user_orders AS SELECT users.id FROM users JOIN orders ON orders.user_id = users.id",
	)
	directory := t.TempDir()

	err := run([]string{"bootstrap", "-dsn", databasePath, "-dialect", "sqlite", "-package", "schemasource", "-output", directory})
	require.NoError(t, err)

	require.FileExists(t, filepath.Join(directory, "users_gen.go"))
	require.FileExists(t, filepath.Join(directory, "orders_gen.go"))
	require.FileExists(t, filepath.Join(directory, "tables_gen.go"))
	require.NoFileExists(t, filepath.Join(directory, "rasql_schema_migrations_gen.go"))
	require.NoFileExists(t, filepath.Join(directory, "user_orders_gen.go"))

	users, err := os.ReadFile(filepath.Join(directory, "users_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(users), "func UsersDef() schema.TableDef {")
	require.Contains(t, string(users), `Name:   "users"`)

	aggregator, err := os.ReadFile(filepath.Join(directory, "tables_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(aggregator), "func Tables() []schema.TableDef {")
	require.Contains(t, string(aggregator), "OrdersDef(),")
	require.Contains(t, string(aggregator), "UsersDef(),")
}

// TestRunBootstrapExcludeSkipsRenamedHistoryTable proves -exclude covers the
// case defaultBootstrapHistorySkip cannot: an application-renamed history
// table.
func TestRunBootstrapExcludeSkipsRenamedHistoryTable(t *testing.T) {
	databasePath := mustCreateSQLite(t,
		"CREATE TABLE users (id INTEGER PRIMARY KEY)",
		"CREATE TABLE app_migrations (id TEXT PRIMARY KEY)",
	)
	directory := t.TempDir()

	err := run([]string{"bootstrap", "-dsn", databasePath, "-dialect", "sqlite", "-package", "schemasource", "-output", directory, "-exclude", "app_migrations"})
	require.NoError(t, err)

	require.FileExists(t, filepath.Join(directory, "users_gen.go"))
	require.NoFileExists(t, filepath.Join(directory, "app_migrations_gen.go"))
}

// TestRunBootstrapTableDescribesOnlyNamedTables proves -table bypasses the
// sweep entirely, including the default history-table skip: a table named
// exactly like the default history table is still described when asked for
// by name.
func TestRunBootstrapTableDescribesOnlyNamedTables(t *testing.T) {
	databasePath := mustCreateSQLite(t,
		"CREATE TABLE users (id INTEGER PRIMARY KEY)",
		"CREATE TABLE orders (id INTEGER PRIMARY KEY)",
		"CREATE TABLE rasql_schema_migrations (id TEXT PRIMARY KEY)",
	)
	directory := t.TempDir()

	err := run([]string{"bootstrap", "-dsn", databasePath, "-dialect", "sqlite", "-package", "schemasource", "-output", directory, "-table", "users", "-table", "rasql_schema_migrations"})
	require.NoError(t, err)

	require.FileExists(t, filepath.Join(directory, "users_gen.go"))
	require.FileExists(t, filepath.Join(directory, "rasql_schema_migrations_gen.go"))
	require.NoFileExists(t, filepath.Join(directory, "orders_gen.go"))
}

func TestRunBootstrapRejectsDuplicateTableFlag(t *testing.T) {
	directory := t.TempDir()
	err := run([]string{"bootstrap", "-dsn", "schema.db", "-package", "generated", "-output", directory, "-table", "users", "-table", "users"})
	require.ErrorContains(t, err, `duplicate -table "users"`)
}

func TestRunBootstrapRejectsDuplicateExcludeFlag(t *testing.T) {
	directory := t.TempDir()
	err := run([]string{"bootstrap", "-dsn", "schema.db", "-package", "generated", "-output", directory, "-exclude", "users", "-exclude", "users"})
	require.ErrorContains(t, err, `duplicate -exclude "users"`)
}

// TestRunBootstrapErrorsWhenSweepFindsNothing covers a database whose only
// table is the migration history table: the default skip leaves nothing to
// describe, and the run must say so rather than write an empty package.
func TestRunBootstrapErrorsWhenSweepFindsNothing(t *testing.T) {
	databasePath := mustCreateSQLite(t, "CREATE TABLE rasql_schema_migrations (id TEXT PRIMARY KEY)")
	directory := t.TempDir()

	err := run([]string{"bootstrap", "-dsn", databasePath, "-dialect", "sqlite", "-package", "schemasource", "-output", directory})
	require.ErrorContains(t, err, "bootstrap found no tables to describe")
	require.Empty(t, mustReadDir(t, directory))
}

// TestRunBootstrapRefusesHandWrittenFileInOutputDirectory covers a
// hand-written file sitting in -output: bootstrap only ever writes into a
// directory it started or one it already owns, so a non-empty -output
// holding a file bootstrap did not write refuses rather than being
// overwritten or treated as a refresh target. A second run against a
// directory bootstrap already wrote to is a refresh instead of a refusal;
// see bootstrap_refresh_test.go.
func TestRunBootstrapRefusesHandWrittenFileInOutputDirectory(t *testing.T) {
	databasePath := mustCreateSQLite(t, "CREATE TABLE users (id INTEGER PRIMARY KEY)")

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "notes.txt"), []byte("hand-written"), 0o600))

	err := run([]string{"bootstrap", "-dsn", databasePath, "-dialect", "sqlite", "-package", "schemasource", "-output", directory})
	require.ErrorContains(t, err, `holds "notes.txt"`)
	require.NoFileExists(t, filepath.Join(directory, "users_gen.go"))
}

func TestRunBootstrapRejectsMissingOutputDirectory(t *testing.T) {
	databasePath := mustCreateSQLite(t, "CREATE TABLE users (id INTEGER PRIMARY KEY)")
	missing := filepath.Join(t.TempDir(), "missing")

	err := run([]string{"bootstrap", "-dsn", databasePath, "-dialect", "sqlite", "-package", "schemasource", "-output", missing})
	require.Error(t, err)
}

func TestRunBootstrapGeneratedFilesCarryTheMarker(t *testing.T) {
	databasePath := mustCreateSQLite(t, "CREATE TABLE users (id INTEGER PRIMARY KEY)")
	directory := t.TempDir()

	require.NoError(t, run([]string{"bootstrap", "-dsn", databasePath, "-dialect", "sqlite", "-package", "schemasource", "-output", directory}))

	for _, name := range []string{"users_gen.go", "tables_gen.go"} {
		source, err := os.ReadFile(filepath.Join(directory, name))
		require.NoError(t, err)
		require.Contains(t, string(source), "// Code generated by rasqlgen; DO NOT EDIT.")
	}
}
