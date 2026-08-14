package rasqlgen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestQueryRefusesToOverwriteABootstrapTableFile confirms `rasqlgen query`
// cannot silently destroy a bootstrap descriptor that happens to land on
// the same path: both commands generate a file named "<table>_gen.go" for
// a table named "users", and only the writing family's own marker on an
// existing destination authorizes replacing it.
func TestQueryRefusesToOverwriteABootstrapTableFile(t *testing.T) {
	databasePath := mustCreateSQLite(t, "CREATE TABLE users (id INTEGER PRIMARY KEY)")
	directory := t.TempDir()

	require.NoError(t, run([]string{"bootstrap", "-dsn", databasePath, "-dialect", "sqlite", "-package", "tables", "-output", directory}))

	output := filepath.Join(directory, "users_gen.go")
	err := run(append(queryOutputArgs(t, directory), "-dialect", "sqlite", "-output", output))
	require.Error(t, err)
	require.ErrorContains(t, err, "bootstrap")
	require.ErrorContains(t, err, "query")

	source, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Contains(t, string(source), "func UsersDef() schema.TableDef")
}

// TestSchemaRefusesToOverwriteABootstrapPackage confirms `rasqlgen schema
// -dsn` cannot silently destroy a bootstrap descriptor package either, which
// is the more likely mistake: a mistyped -output pointed at the description
// package a `bootstrap` command already owns.
func TestSchemaRefusesToOverwriteABootstrapPackage(t *testing.T) {
	databasePath := mustCreateSQLite(t, "CREATE TABLE users (id INTEGER PRIMARY KEY)")
	directory := t.TempDir()

	require.NoError(t, run([]string{"bootstrap", "-dsn", databasePath, "-dialect", "sqlite", "-package", "tables", "-output", directory}))

	err := run([]string{"schema", "-dsn", databasePath, "-dialect", "sqlite", "-table", "users", "-package", "tables", "-output", directory})
	require.Error(t, err)
	require.ErrorContains(t, err, "bootstrap")
	require.ErrorContains(t, err, "schema")

	source, err := os.ReadFile(filepath.Join(directory, "users_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(source), "func UsersDef() schema.TableDef")
}

// TestBootstrapRefusesADirectoryHoldingSchemaOutput confirms a `bootstrap`
// refresh into a directory `schema` already wrote refuses through
// ValidateDescriptionPackageOwnership, naming the file it does not
// recognize as its own.
func TestBootstrapRefusesADirectoryHoldingSchemaOutput(t *testing.T) {
	databasePath := mustCreateSQLite(t, "CREATE TABLE users (id INTEGER PRIMARY KEY)")
	directory := t.TempDir()

	require.NoError(t, run([]string{"schema", "-dsn", databasePath, "-dialect", "sqlite", "-table", "users", "-package", "tables", "-output", directory}))

	err := run([]string{"bootstrap", "-dsn", databasePath, "-dialect", "sqlite", "-package", "tables", "-output", directory})
	require.Error(t, err)
	require.ErrorContains(t, err, "refusing to refresh a directory it does not own")
	require.ErrorContains(t, err, "_gen.go")
}
