package rasqlgen_test

import (
	"bytes"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	"github.com/stretchr/testify/require"
)

// TestBootstrapRefreshReportsThenWritesSQLiteDrift is the end-to-end test
// for a bootstrap refresh: a first bootstrap run writes a package for
// three tables, an edit to hints.go is made by hand, the database then
// gains a table, loses a table, gains a column, and loses a column, and a
// bare re-run against that drift only reports it -- writing nothing -- while
// -write applies exactly what the report described, including the deleted
// table's file and its removal from the aggregator. The refreshed package
// is then proven to still build and still be valid `rasqlgen schema
// -source` input, and the hand edit to hints.go is proven to have
// survived.
func TestBootstrapRefreshReportsThenWritesSQLiteDrift(t *testing.T) {
	moduleDir := newBootstrapRoundTripModule(t)
	databasePath := filepath.Join(moduleDir, "schema.db")

	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	for _, statement := range []string{
		"CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)",
		"CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users (id), notes TEXT)",
		"CREATE TABLE legacy_users (id INTEGER PRIMARY KEY)",
	} {
		_, err = database.ExecContext(t.Context(), statement)
		require.NoError(t, err)
	}
	require.NoError(t, database.Close())

	t.Chdir(moduleDir)
	outputDir := filepath.Join(moduleDir, "internal", "tables")

	var buffer bytes.Buffer
	require.NoError(t, rasqlgen.Run([]string{"bootstrap", "-dsn", databasePath, "-dialect", "sqlite", "-package", "tables", "-output", "internal/tables"}, &buffer), buffer.String())
	require.FileExists(t, filepath.Join(outputDir, "users_gen.go"))
	require.FileExists(t, filepath.Join(outputDir, "orders_gen.go"))
	require.FileExists(t, filepath.Join(outputDir, "legacy_users_gen.go"))
	require.FileExists(t, filepath.Join(outputDir, "hints.go"))

	hintsPath := filepath.Join(outputDir, "hints.go")
	edited := []byte("package tables\n\nimport \"github.com/lestrrat-go/rasql/schema\"\n\nvar Hints = map[string]schema.TableHint{\"users\": {RowName: \"User\"}}\n")
	require.NoError(t, os.WriteFile(hintsPath, edited, 0o600))

	// A bare re-run against an unchanged database reports nothing and
	// writes nothing.
	buffer.Reset()
	require.NoError(t, rasqlgen.Run([]string{"bootstrap", "-dsn", databasePath, "-dialect", "sqlite", "-package", "tables", "-output", "internal/tables"}, &buffer))
	require.Empty(t, buffer.String(), "an unchanged database must report nothing")
	beforeDrift := mustSnapshotDirectory(t, outputDir)

	database, err = sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	for _, statement := range []string{
		"CREATE TABLE products (id INTEGER PRIMARY KEY, name TEXT NOT NULL)",
		"DROP TABLE legacy_users",
		"ALTER TABLE users ADD COLUMN phone TEXT",
		"ALTER TABLE orders DROP COLUMN notes",
	} {
		_, err = database.ExecContext(t.Context(), statement)
		require.NoError(t, err)
	}
	require.NoError(t, database.Close())

	// A report-only run describes the drift and changes nothing on disk.
	buffer.Reset()
	require.NoError(t, rasqlgen.Run([]string{"bootstrap", "-dsn", databasePath, "-dialect", "sqlite", "-package", "tables", "-output", "internal/tables"}, &buffer))
	report := buffer.String()
	require.Contains(t, report, `+ table "products"`)
	require.Contains(t, report, `- table "legacy_users"`)
	require.Contains(t, report, `~ table "users"`)
	require.Contains(t, report, `+ column "phone"`)
	require.Contains(t, report, `~ table "orders"`)
	require.Contains(t, report, `- column "notes"`)
	afterReportOnly := mustSnapshotDirectory(t, outputDir)
	require.Equal(t, beforeDrift, afterReportOnly, "a report-only refresh must not change any file")

	// -write applies exactly what the report described.
	buffer.Reset()
	require.NoError(t, rasqlgen.Run([]string{"bootstrap", "-dsn", databasePath, "-dialect", "sqlite", "-package", "tables", "-output", "internal/tables", "-write"}, &buffer))
	require.FileExists(t, filepath.Join(outputDir, "products_gen.go"))
	require.NoFileExists(t, filepath.Join(outputDir, "legacy_users_gen.go"))

	aggregator, err := os.ReadFile(filepath.Join(outputDir, "tables_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(aggregator), "ProductsDef(),")
	require.Contains(t, string(aggregator), "UsersDef(),")
	require.Contains(t, string(aggregator), "OrdersDef(),")
	require.NotContains(t, string(aggregator), "LegacyUsersDef")

	usersSource, err := os.ReadFile(filepath.Join(outputDir, "users_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(usersSource), `{Name: "phone"`)

	ordersSource, err := os.ReadFile(filepath.Join(outputDir, "orders_gen.go"))
	require.NoError(t, err)
	require.NotContains(t, string(ordersSource), `{Name: "notes"`)

	gotHints, err := os.ReadFile(hintsPath)
	require.NoError(t, err)
	require.Equal(t, edited, gotHints, "a refresh must never rewrite hints.go")

	// A second -write against the now-unchanged database reports and
	// writes nothing.
	settled := mustSnapshotDirectory(t, outputDir)
	buffer.Reset()
	require.NoError(t, rasqlgen.Run([]string{"bootstrap", "-dsn", databasePath, "-dialect", "sqlite", "-package", "tables", "-output", "internal/tables", "-write"}, &buffer))
	require.Empty(t, buffer.String())
	require.Equal(t, settled, mustSnapshotDirectory(t, outputDir))

	// The refreshed package still builds and is still valid `rasqlgen
	// schema -source` input.
	buffer.Reset()
	require.NoError(t, rasqlgen.Run([]string{"schema", "-source", "./internal/tables", "-package", "store", "-output", "internal/store"}, &buffer), buffer.String())
	require.FileExists(t, filepath.Join(moduleDir, "internal", "store", "schema_gen.go"))

	build := exec.CommandContext(t.Context(), "go", "build", "./...")
	build.Dir = moduleDir
	output, err := build.CombinedOutput()
	require.NoError(t, err, string(output))
}

// mustSnapshotDirectory reads every regular file directly inside directory
// into a name-to-contents map, for a byte-identical comparison across an
// operation that must be a no-op.
func mustSnapshotDirectory(t *testing.T, directory string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	snapshot := make(map[string]string, len(entries))
	for _, entry := range entries {
		contents, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		require.NoError(t, err)
		snapshot[entry.Name()] = string(contents)
	}
	return snapshot
}
