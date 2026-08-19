package rasqlgen_test

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// TestGoRunGeneratesFromAConfigFile is the load-bearing test of the settings
// file: reading JSON is easy to get superficially right, so this proves it
// by generating a real package in a real module and building it, not by
// comparing structs.
//
// It builds a scratch consumer module the way generate/acceptance_test.go's
// TestGeneratedStorePackageCompilesAndRuns does (copy this repository's
// go.mod/go.sum, rewrite the module line, append a replace back to this
// checkout, so no "go mod tidy" is needed), writes a rasql.json naming a row
// type and a static query, runs the command through "go run" exactly as a
// real consumer would, and drives -check through both the clean and the
// stale state.
func TestGoRunGeneratesFromAConfigFile(t *testing.T) {
	moduleDir := t.TempDir()
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)

	repoGoMod, err := os.ReadFile(filepath.Join(repository, "go.mod"))
	require.NoError(t, err)
	module := strings.Replace(string(repoGoMod), "module github.com/lestrrat-go/rasql\n", "module example.com/consumer\n", 1)
	module += "\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte(module), 0o600))

	repoGoSum, err := os.ReadFile(filepath.Join(repository, "go.sum"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "go.sum"), repoGoSum, 0o600))

	// GOFLAGS=-buildvcs=false: the scratch module lives under t.TempDir(),
	// and Go's VCS auto-detection has been observed to still try (and fail)
	// to run "git status" when a stray .git directory sits somewhere above
	// the OS temp directory -- see generate/descriptor_roundtrip_test.go's
	// identical guard.
	env := append(os.Environ(), "GOFLAGS=-buildvcs=false")

	databasePath := filepath.Join(moduleDir, "schema.db")
	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE audit_log (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	// rasql_schema_migrations is catalog.FromDatabase's own default history
	// table exclusion; it must not show up as a generated file below.
	_, err = database.ExecContext(t.Context(), "CREATE TABLE rasql_schema_migrations (version TEXT PRIMARY KEY)")
	require.NoError(t, err)
	require.NoError(t, database.Close())

	require.NoError(t, os.MkdirAll(filepath.Join(moduleDir, "queries"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "queries", "user_by_email.sql"),
		[]byte(`SELECT id, email FROM users WHERE email = {{bind "email"}}`), 0o600))

	// Every setting this project has lives here: the package, the output
	// directory, the dialect, the excluded table, the row-type override, and
	// the static query whose output file name is left to be derived.
	config := `{
  "package": "store",
  "output": "internal/store",
  "dialect": "sqlite",
  "tables": {
    "exclude": ["audit_log"],
    "row_names": {"users": "User"}
  },
  "queries": [
    {"input": "queries/user_by_email.sql", "function": "UserByEmail"}
  ]
}
`
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "rasql.json"), []byte(config), 0o600))

	// The command line carries only what the file deliberately does not: the
	// DSN, and which of write-or-check this run is.
	generate := func(extra ...string) ([]byte, error) {
		args := append([]string{"run", "github.com/lestrrat-go/rasql/cmd/rasql", "codegen", "generate", "-dsn", databasePath}, extra...)
		command := exec.CommandContext(t.Context(), "go", args...)
		command.Dir = moduleDir
		command.Env = env
		return command.CombinedOutput()
	}

	output, err := generate()
	require.NoError(t, err, string(output))

	storeDir := filepath.Join(moduleDir, "internal", "store")
	require.Equal(t, []string{"posts_gen.go", "schema_gen.go", "schema_gen_test.go", "user_by_email_gen.go", "users_gen.go"},
		generatedFileNames(t, storeDir),
		"the excluded table generates no file, and the query's output name is derived from its input")

	source, err := os.ReadFile(filepath.Join(storeDir, "users_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(source), "type User struct", "the configured row name reaches the generated type")
	require.NotContains(t, string(source), "type UsersRow struct")

	source, err = os.ReadFile(filepath.Join(storeDir, "user_by_email_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(source), "func UserByEmail(")

	command := exec.CommandContext(t.Context(), "go", "build", "./...")
	command.Dir = moduleDir
	command.Env = env
	output, err = command.CombinedOutput()
	require.NoError(t, err, string(output))

	output, err = generate("-check")
	require.NoError(t, err, string(output))

	database, err = sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "ALTER TABLE users ADD COLUMN nickname TEXT")
	require.NoError(t, err)
	require.NoError(t, database.Close())

	output, err = generate("-check")
	require.Error(t, err)
	require.Contains(t, string(output), "is stale")

	output, err = generate()
	require.NoError(t, err, string(output))
	output, err = generate("-check")
	require.NoError(t, err, string(output))
}

// TestGoRunFlagOverridesTheConfigFile requires a flag the user typed to beat
// the same setting in the file, so a one-off run needs no edit to it.
func TestGoRunFlagOverridesTheConfigFile(t *testing.T) {
	moduleDir := t.TempDir()
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)

	repoGoMod, err := os.ReadFile(filepath.Join(repository, "go.mod"))
	require.NoError(t, err)
	module := strings.Replace(string(repoGoMod), "module github.com/lestrrat-go/rasql\n", "module example.com/consumer\n", 1)
	module += "\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte(module), 0o600))
	repoGoSum, err := os.ReadFile(filepath.Join(repository, "go.sum"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "go.sum"), repoGoSum, 0o600))
	env := append(os.Environ(), "GOFLAGS=-buildvcs=false")

	databasePath := filepath.Join(moduleDir, "schema.db")
	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	require.NoError(t, database.Close())

	config := `{"package": "store", "output": "internal/store", "dialect": "sqlite"}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "rasql.json"), []byte(config), 0o600))

	command := exec.CommandContext(t.Context(), "go", "run", "github.com/lestrrat-go/rasql/cmd/rasql",
		"codegen", "generate", "-dsn", databasePath, "-output", "internal/elsewhere")
	command.Dir = moduleDir
	command.Env = env
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))

	require.FileExists(t, filepath.Join(moduleDir, "internal", "elsewhere", "users_gen.go"))
	require.NoDirExists(t, filepath.Join(moduleDir, "internal", "store"))
}

// generatedFileNames reports the sorted names of the regular files directly
// inside dir, failing the test if dir holds a subdirectory.
func generatedFileNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		require.Falsef(t, entry.IsDir(), "unexpected directory %s in %s", entry.Name(), dir)
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}
