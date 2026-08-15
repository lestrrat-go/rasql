package rasqlgen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRunSchemaSweepsSQLite exercises `schema -dsn` with no -table: it
// mirrors TestRunBootstrapSweepsSQLite, since both commands resolve a
// no-table sweep through the same sweepTables helper, including the default
// skip of the migration history table.
func TestRunSchemaSweepsSQLite(t *testing.T) {
	databasePath := mustCreateSQLite(t,
		"CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)",
		"CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users (id))",
		"CREATE TABLE rasql_schema_migrations (id TEXT PRIMARY KEY)",
		"CREATE VIEW user_orders AS SELECT users.id FROM users JOIN orders ON orders.user_id = users.id",
	)
	directory := t.TempDir()

	err := run([]string{"schema", "-dsn", databasePath, "-dialect", "sqlite", "-package", "store", "-output", directory})
	require.NoError(t, err)

	require.FileExists(t, filepath.Join(directory, "users_gen.go"))
	require.FileExists(t, filepath.Join(directory, "orders_gen.go"))
	require.FileExists(t, filepath.Join(directory, "schema_gen.go"))
	require.NoFileExists(t, filepath.Join(directory, "rasql_schema_migrations_gen.go"))
	require.NoFileExists(t, filepath.Join(directory, "user_orders_gen.go"))

	descriptor, err := os.ReadFile(filepath.Join(directory, "schema_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(descriptor), "usersTable")
	require.Contains(t, string(descriptor), "ordersTable")
}

// TestRunSchemaExcludeSkipsRenamedHistoryTable proves -exclude covers the
// case the default history-table skip cannot: an application-renamed
// history table.
func TestRunSchemaExcludeSkipsRenamedHistoryTable(t *testing.T) {
	databasePath := mustCreateSQLite(t,
		"CREATE TABLE users (id INTEGER PRIMARY KEY)",
		"CREATE TABLE app_migrations (id TEXT PRIMARY KEY)",
	)
	directory := t.TempDir()

	err := run([]string{"schema", "-dsn", databasePath, "-dialect", "sqlite", "-package", "store", "-output", directory, "-exclude", "app_migrations"})
	require.NoError(t, err)

	require.FileExists(t, filepath.Join(directory, "users_gen.go"))
	require.NoFileExists(t, filepath.Join(directory, "app_migrations_gen.go"))
}

// TestRunSchemaExcludeUnknownTableSweepsEverything pins what an -exclude
// naming a table the database does not have does: sweepTables matches an
// exclusion by name against what inspector.TableNames reported, so a name
// that matches nothing removes nothing and the run still describes every
// base table. Nothing checks an -exclude value against the database, so a
// misspelled name is accepted in silence rather than reported.
func TestRunSchemaExcludeUnknownTableSweepsEverything(t *testing.T) {
	databasePath := mustCreateSQLite(t,
		"CREATE TABLE users (id INTEGER PRIMARY KEY)",
		"CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users (id))",
	)
	directory := t.TempDir()

	err := run([]string{"schema", "-dsn", databasePath, "-dialect", "sqlite", "-package", "store", "-output", directory, "-exclude", "no_such_table"})
	require.NoError(t, err)

	require.FileExists(t, filepath.Join(directory, "users_gen.go"))
	require.FileExists(t, filepath.Join(directory, "orders_gen.go"))
	require.FileExists(t, filepath.Join(directory, "schema_gen.go"))
}

// TestRunSchemaExcludeEveryTableErrors covers -exclude naming every base
// table the sweep would otherwise describe: the sweep empties out the same
// way TestRunSchemaErrorsWhenSweepFindsNothing's default history-table skip
// empties it, so the run must report that and write nothing.
func TestRunSchemaExcludeEveryTableErrors(t *testing.T) {
	databasePath := mustCreateSQLite(t,
		"CREATE TABLE users (id INTEGER PRIMARY KEY)",
		"CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users (id))",
	)
	directory := t.TempDir()

	err := run([]string{"schema", "-dsn", databasePath, "-dialect", "sqlite", "-package", "store", "-output", directory, "-exclude", "users", "-exclude", "orders"})
	require.ErrorContains(t, err, "schema found no tables to describe")
	require.Empty(t, mustReadDir(t, directory))
}

// TestRunSchemaRejectsTableAndExcludeTogether covers the same mutual
// exclusivity `bootstrap` enforces between -table and -exclude.
func TestRunSchemaRejectsTableAndExcludeTogether(t *testing.T) {
	directory := t.TempDir()
	err := run([]string{"schema", "-dsn", "schema.db", "-package", "store", "-output", directory, "-table", "users", "-exclude", "orders"})
	require.ErrorContains(t, err, "schema accepts -table or -exclude, not both")
}

func TestRunSchemaRejectsDuplicateExcludeFlag(t *testing.T) {
	directory := t.TempDir()
	err := run([]string{"schema", "-dsn", "schema.db", "-package", "store", "-output", directory, "-exclude", "users", "-exclude", "users"})
	require.ErrorContains(t, err, `duplicate -exclude "users"`)
}

// TestRunSchemaErrorsWhenSweepFindsNothing covers a database whose only
// table is the migration history table: the default skip leaves nothing to
// generate, and the run must say so rather than write an empty package.
func TestRunSchemaErrorsWhenSweepFindsNothing(t *testing.T) {
	databasePath := mustCreateSQLite(t, "CREATE TABLE rasql_schema_migrations (id TEXT PRIMARY KEY)")
	directory := t.TempDir()

	err := run([]string{"schema", "-dsn", databasePath, "-dialect", "sqlite", "-package", "store", "-output", directory})
	require.ErrorContains(t, err, "schema found no tables to describe")
	require.Empty(t, mustReadDir(t, directory))
}

// TestRunSchemaCreatesMissingOutputDirectory covers a -output that does not
// exist yet, the same way TestRunBootstrapCreatesMissingOutputDirectory
// does for bootstrap.
func TestRunSchemaCreatesMissingOutputDirectory(t *testing.T) {
	databasePath := mustCreateSQLite(t, "CREATE TABLE users (id INTEGER PRIMARY KEY)")
	missing := filepath.Join(t.TempDir(), "missing", "nested")

	err := run([]string{"schema", "-dsn", databasePath, "-dialect", "sqlite", "-table", "users", "-package", "store", "-output", missing})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(missing, "users_gen.go"))
}

// TestRunSchemaExcludeRejectedWithSource covers -exclude given alongside
// -source, which has no sweep to filter: -source already generates every
// table in the schema package when -table is empty (schemaSourceProgram's
// own filterTables), so a silently ignored -exclude there would look like
// it worked while doing nothing.
func TestRunSchemaExcludeRejectedWithSource(t *testing.T) {
	directory := t.TempDir()
	err := run([]string{"schema", "-source", "./internal/tables", "-package", "store", "-output", directory, "-exclude", "users"})
	require.ErrorContains(t, err, "schema -exclude is not supported with -source")
}
