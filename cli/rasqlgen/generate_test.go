package rasqlgen_test

import (
	"bytes"
	"database/sql"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// newGenerateDatabase writes a SQLite database holding two tables and
// returns its path, so a generate run has a real live schema to read rather
// than a fixture describing one.
func newGenerateDatabase(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "schema.db")
	database, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	_, err = database.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE posts (id INTEGER PRIMARY KEY, title TEXT NOT NULL)")
	require.NoError(t, err)
	return path
}

// runGenerate calls the generate command with the flags every case shares,
// plus whatever the case adds, and returns what the command printed.
func runGenerate(t *testing.T, dir, databasePath string, extra ...string) (string, error) {
	t.Helper()
	args := append([]string{
		"generate",
		"-dsn", databasePath,
		"-dialect", "sqlite",
		"-package", "store",
		"-output", "internal/store",
		"-root", dir,
	}, extra...)
	var output, diagnostics bytes.Buffer
	err := rasqlgen.Run(args, &output, &diagnostics)
	return output.String() + diagnostics.String(), err
}

// TestGenerateWritesTheStoreFromALiveDatabase is the load-bearing test of
// the generate command: it must reach a real database, write the package,
// and then report that same package as current, with no program in between.
func TestGenerateWritesTheStoreFromALiveDatabase(t *testing.T) {
	dir := t.TempDir()
	databasePath := newGenerateDatabase(t, dir)

	output, err := runGenerate(t, dir, databasePath)
	require.NoError(t, err, output)
	require.Contains(t, output, "wrote internal/store from 2 tables")

	storeDir := filepath.Join(dir, "internal", "store")
	for _, name := range []string{"users_gen.go", "posts_gen.go", "schema_gen.go"} {
		require.FileExists(t, filepath.Join(storeDir, name))
	}

	output, err = runGenerate(t, dir, databasePath, "-check")
	require.NoError(t, err, output)
	require.Contains(t, output, "internal/store is up to date")
}

// TestGenerateCheckReportsAStaleStore requires -check to fail, rather than
// silently rewrite, once the checked-in package stops matching the database
// it was generated from. This is the check a CI job runs.
func TestGenerateCheckReportsAStaleStore(t *testing.T) {
	dir := t.TempDir()
	databasePath := newGenerateDatabase(t, dir)

	output, err := runGenerate(t, dir, databasePath)
	require.NoError(t, err, output)

	path := filepath.Join(dir, "internal", "store", "schema_gen.go")
	source, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(source, []byte("\n// edited\n")...), 0o644))

	_, err = runGenerate(t, dir, databasePath, "-check")
	require.ErrorIs(t, err, generate.ErrStale)
}

// TestGenerateSelectsTables requires -include and -exclude to reach
// catalog.Options rather than being accepted and ignored.
func TestGenerateSelectsTables(t *testing.T) {
	t.Run("include", func(t *testing.T) {
		dir := t.TempDir()
		databasePath := newGenerateDatabase(t, dir)

		output, err := runGenerate(t, dir, databasePath, "-include", "users")
		require.NoError(t, err, output)
		require.Contains(t, output, "wrote internal/store from 1 table")
		require.FileExists(t, filepath.Join(dir, "internal", "store", "users_gen.go"))
		require.NoFileExists(t, filepath.Join(dir, "internal", "store", "posts_gen.go"))
	})

	t.Run("exclude", func(t *testing.T) {
		dir := t.TempDir()
		databasePath := newGenerateDatabase(t, dir)

		output, err := runGenerate(t, dir, databasePath, "-exclude", "posts")
		require.NoError(t, err, output)
		require.FileExists(t, filepath.Join(dir, "internal", "store", "users_gen.go"))
		require.NoFileExists(t, filepath.Join(dir, "internal", "store", "posts_gen.go"))
	})
}

// TestGenerateResolvesOutputAgainstTheModuleRoot requires an -output with no
// -root to land where generate.Store itself puts it, which is the module
// root above the working directory rather than the working directory.
func TestGenerateResolvesOutputAgainstTheModuleRoot(t *testing.T) {
	dir := t.TempDir()
	databasePath := newGenerateDatabase(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/consumer\n\ngo 1.26.0\n"), 0o600))
	subdirectory := filepath.Join(dir, "cmd", "app")
	require.NoError(t, os.MkdirAll(subdirectory, 0o755))
	t.Chdir(subdirectory)

	var output, diagnostics bytes.Buffer
	err := rasqlgen.Run([]string{
		"generate",
		"-dsn", databasePath,
		"-dialect", "sqlite",
		"-package", "store",
		"-output", "internal/store",
	}, &output, &diagnostics)
	require.NoError(t, err, output.String()+diagnostics.String())
	require.FileExists(t, filepath.Join(dir, "internal", "store", "users_gen.go"))
}

// TestGeneratePruneRefusesAnOrphanWhenDisabled requires -prune=false to
// reach generate.Store.Prune, where a run that would delete a file it no
// longer writes is refused and the file named instead.
func TestGeneratePruneRefusesAnOrphanWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	databasePath := newGenerateDatabase(t, dir)

	output, err := runGenerate(t, dir, databasePath)
	require.NoError(t, err, output)

	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	defer func() { _ = database.Close() }()
	_, err = database.ExecContext(t.Context(), "DROP TABLE posts")
	require.NoError(t, err)

	_, err = runGenerate(t, dir, databasePath, "-prune=false")
	require.Error(t, err)
	require.Contains(t, err.Error(), "posts_gen.go")

	output, err = runGenerate(t, dir, databasePath)
	require.NoError(t, err, output)
	require.NoFileExists(t, filepath.Join(dir, "internal", "store", "posts_gen.go"))
}

// TestGenerateRejectsBadFlags pins the diagnostic for each flag mistake,
// because a command that reports "-dsn is required" for a misspelled
// dialect sends its user looking in the wrong place.
func TestGenerateRejectsBadFlags(t *testing.T) {
	testCases := []struct {
		name     string
		args     []string
		expected string
	}{
		{
			name:     "unsupported dialect",
			args:     []string{"generate", "-dsn", "app.db", "-dialect", "oracle", "-package", "store", "-output", "internal/store"},
			expected: `generate: unsupported -dialect "oracle"; want postgresql, postgres, mysql, or sqlite`,
		},
		{
			name:     "missing dialect",
			args:     []string{"generate", "-dsn", "app.db", "-package", "store", "-output", "internal/store"},
			expected: `generate: unsupported -dialect ""; want postgresql, postgres, mysql, or sqlite`,
		},
		{
			name:     "missing dsn",
			args:     []string{"generate", "-dialect", "sqlite", "-package", "store", "-output", "internal/store"},
			expected: "generate: -dsn is required",
		},
		{
			name:     "package is not an identifier",
			args:     []string{"generate", "-dsn", "app.db", "-dialect", "sqlite", "-package", "store/v2", "-output", "internal/store"},
			expected: `generate: -package "store/v2" must be a Go identifier`,
		},
		{
			name:     "blank package identifier",
			args:     []string{"generate", "-dsn", "app.db", "-dialect", "sqlite", "-package", "_", "-output", "internal/store"},
			expected: `generate: -package "_" must be a Go identifier`,
		},
		{
			name:     "missing output",
			args:     []string{"generate", "-dsn", "app.db", "-dialect", "sqlite", "-package", "store"},
			expected: "generate: -output is required",
		},
		{
			name:     "empty table name",
			args:     []string{"generate", "-dsn", "app.db", "-dialect", "sqlite", "-package", "store", "-output", "internal/store", "-include", "users,,posts"},
			expected: `generate: -include "users,,posts" holds an empty table name`,
		},
		{
			name:     "non-positive timeout",
			args:     []string{"generate", "-dsn", "app.db", "-dialect", "sqlite", "-package", "store", "-output", "internal/store", "-timeout", "0s"},
			expected: "generate: -timeout 0s must be positive",
		},
		{
			name:     "leftover arguments",
			args:     []string{"generate", "-dsn", "app.db", "-dialect", "sqlite", "-package", "store", "-output", "internal/store", "extra"},
			expected: `unexpected arguments: ["extra"]`,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			// A refused run must not have written anything on its way to
			// the refusal, so it runs in a directory of its own that the
			// assertion below can read back.
			dir := t.TempDir()
			t.Chdir(dir)
			var output, diagnostics bytes.Buffer
			err := rasqlgen.Run(testCase.args, &output, &diagnostics)
			require.EqualError(t, err, testCase.expected)
			entries, readErr := os.ReadDir(dir)
			require.NoError(t, readErr)
			require.Empty(t, entries, "a refused run must write nothing")
		})
	}
}

// TestGenerateKeepsThePasswordOutOfAFailure requires a failed run to report
// what failed without reprinting the password it was handed, on both the
// path where the driver refuses the DSN and the path where it accepts the
// DSN and then fails to use it.
//
// Both drivers below mask the password in these particular messages
// themselves; the command still passes every DSN-carrying error through
// internal/dsnredact, which is what holds this promise for a driver that
// quotes back what it was given. dsnredact's own test covers the
// replacement, so this test states the promise a user sees rather than
// asserting the marker text.
//
// Neither case reaches the network: one DSN is malformed, and the other
// names a port that is not a number, so both fail before anything is
// dialed.
func TestGenerateKeepsThePasswordOutOfAFailure(t *testing.T) {
	testCases := []struct {
		name    string
		dialect string
		dsn     string
	}{
		{name: "driver refuses the dsn", dialect: "mysql", dsn: "user:secret@tcp(example.test:3306/database"},
		{name: "driver cannot use the dsn", dialect: "postgresql", dsn: "postgres://user:secret@example.test:notaport/database"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			var output, diagnostics bytes.Buffer
			err := rasqlgen.Run([]string{
				"generate",
				"-dsn", testCase.dsn,
				"-dialect", testCase.dialect,
				"-package", "store",
				"-output", "internal/store",
			}, &output, &diagnostics)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "secret")
		})
	}
}

// TestGenerateHelpGoesToCommandOutput requires the unified command to name
// itself in the generate usage block, and to print a help request as
// command output rather than as a diagnostic.
func TestGenerateHelpGoesToCommandOutput(t *testing.T) {
	var output, diagnostics bytes.Buffer
	err := rasqlgen.Run([]string{"generate", "-h"}, &output, &diagnostics)
	require.ErrorIs(t, err, flag.ErrHelp)
	require.Contains(t, output.String(), "Usage of rasql codegen generate:")
	require.Empty(t, diagnostics.String())
}
