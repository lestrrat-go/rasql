package rasqlgen_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/stretchr/testify/require"
)

// TestSQLiteGenerateScratchThenCheckThroughPublicRun exercises the public rasqlgen.Run entry
// point end to end: -scratch builds and drops a real SQLite database, applies the migration for
// real, and writes the store and rasql.sum; the offline check that follows then reads that sum
// back and consults no database.
func TestSQLiteGenerateScratchThenCheckThroughPublicRun(t *testing.T) {
	root := t.TempDir()
	writeMigration(t, root, "001_init", "CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL);\n")
	configPath := filepath.Join(root, "rasql.json")
	writeGenerateConfig(t, configPath, "store", "migrations")

	var output, diagnostics bytes.Buffer
	err := rasqlgen.Run([]string{"generate", "-config", configPath, "-scratch"}, &output, &diagnostics)
	require.NoError(t, err, diagnostics.String())
	require.FileExists(t, filepath.Join(root, "internal", "store", "users_gen.go"))
	require.FileExists(t, filepath.Join(root, "internal", "store", "rasql.sum"))

	output.Reset()
	diagnostics.Reset()
	err = rasqlgen.Run([]string{"check", "-config", configPath}, &output, &diagnostics)
	require.NoError(t, err, diagnostics.String())
}

// TestGenerateScratchDefaultsToCompactEmitter proves an unset emitter setting still renders
// through the compact emitter.
func TestGenerateScratchDefaultsToCompactEmitter(t *testing.T) {
	root := t.TempDir()
	writeMigration(t, root, "001_init", "CREATE TABLE users (id INTEGER PRIMARY KEY);\n")
	configPath := filepath.Join(root, "rasql.json")
	config := map[string]any{"dialect": "sqlite", "migrations": "migrations", "package": "store", "output": "internal/store"}
	configBytes, err := json.Marshal(config)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configBytes, 0o600))

	var output, diagnostics bytes.Buffer
	require.NoError(t, rasqlgen.Run([]string{"generate", "-config", configPath, "-scratch"}, &output, &diagnostics), diagnostics.String())
	source := readFile(t, filepath.Join(root, "internal", "store", "users_gen.go"))
	require.Contains(t, string(source), "MustTableOf")
}

// TestCheckOfflineReportsMigrationsDriftThroughPublicRun proves the offline check, reached
// through the public entry point, detects a migration file that changed since the last generate
// and names the migrations group, without touching rasql.sum or the generated files.
func TestCheckOfflineReportsMigrationsDriftThroughPublicRun(t *testing.T) {
	root := t.TempDir()
	migration := writeMigration(t, root, "001_init", "CREATE TABLE users (id INTEGER PRIMARY KEY);\n")
	configPath := filepath.Join(root, "rasql.json")
	writeGenerateConfig(t, configPath, "store", "migrations")

	var output, diagnostics bytes.Buffer
	require.NoError(t, rasqlgen.Run([]string{"generate", "-config", configPath, "-scratch"}, &output, &diagnostics), diagnostics.String())
	sumBefore := readFile(t, filepath.Join(root, "internal", "store", "rasql.sum"))

	require.NoError(t, os.WriteFile(migration, []byte("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);\n"), 0o600))
	output.Reset()
	diagnostics.Reset()
	err := rasqlgen.Run([]string{"check", "-config", configPath}, &output, &diagnostics)
	require.Error(t, err)
	require.ErrorIs(t, err, generate.ErrStale)
	require.Contains(t, err.Error(), "migrations")
	require.Equal(t, sumBefore, readFile(t, filepath.Join(root, "internal", "store", "rasql.sum")))
}

// TestGenerateScratchPropagatesCancellationBeforePublication proves a context canceled before
// generate writes anything leaves nothing behind, through the public rasqlgen.RunContext.
func TestGenerateScratchPropagatesCancellationBeforePublication(t *testing.T) {
	root := t.TempDir()
	writeMigration(t, root, "001_init", "CREATE TABLE users (id INTEGER PRIMARY KEY);\n")
	configPath := filepath.Join(root, "rasql.json")
	writeGenerateConfig(t, configPath, "store", "migrations")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output, diagnostics bytes.Buffer
	err := rasqlgen.RunContext(ctx, []string{"generate", "-config", configPath, "-scratch"}, &output, &diagnostics)
	require.ErrorIs(t, err, context.Canceled)
	require.NoDirExists(t, filepath.Join(root, "internal", "store"))
}

// writeMigration writes one migration in internal/migrationdir layout -- a directory named id
// holding a single 1.up.sql -- and returns that file's path.
func writeMigration(t *testing.T, root, id, upSQL string) string {
	t.Helper()
	dir := filepath.Join(root, "migrations", id)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	path := filepath.Join(dir, "1.up.sql")
	require.NoError(t, os.WriteFile(path, []byte(upSQL), 0o600))
	return path
}

func writeGenerateConfig(t *testing.T, path, packageName, migrationsDir string) {
	t.Helper()
	config := map[string]any{
		"dialect": "sqlite", "migrations": migrationsDir,
		"package": packageName, "output": "internal/store", "emitter": "compact",
	}
	b, err := json.Marshal(config)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, b, 0o600))
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}
