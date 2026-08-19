package rasqlgen_test

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// configModule writes a scratch module holding a go.mod, a SQLite database
// with one table, and whatever settings file the case needs. It returns the
// module directory and the database path.
//
// A go.mod is what makes the module root findable, which is where the
// default settings file is looked for and what a relative output resolves
// against.
func configModule(t *testing.T, settings string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/consumer\n\ngo 1.26.0\n"), 0o600))
	if settings != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "rasql.json"), []byte(settings), 0o600))
	}

	databasePath := filepath.Join(dir, "schema.db")
	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	_, err = database.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE audit_log (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	return dir, databasePath
}

// runConfigured runs generate from inside dir, so the default settings file
// is found the way a real run finds it.
func runConfigured(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	t.Chdir(dir)
	var output, diagnostics bytes.Buffer
	err := rasqlgen.Run(append([]string{"generate"}, args...), &output, &diagnostics)
	return output.String() + diagnostics.String(), err
}

// TestConfigSuppliesEverySetting requires the settings file alone, with only
// a DSN on the command line, to drive a complete run.
func TestConfigSuppliesEverySetting(t *testing.T) {
	dir, databasePath := configModule(t, `{
  "package": "store",
  "output": "internal/store",
  "dialect": "sqlite",
  "tables": {"exclude": ["audit_log"], "row_names": {"users": "User"}}
}`)

	output, err := runConfigured(t, dir, "-dsn", databasePath)
	require.NoError(t, err, output)

	source, err := os.ReadFile(filepath.Join(dir, "internal", "store", "users_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(source), "type User struct")
	require.NoFileExists(t, filepath.Join(dir, "internal", "store", "audit_log_gen.go"))
}

// TestConfigIsOptional requires a project that says everything on the
// command line to need no settings file at all.
func TestConfigIsOptional(t *testing.T) {
	dir, databasePath := configModule(t, "")

	output, err := runConfigured(t, dir, "-dsn", databasePath, "-dialect", "sqlite", "-package", "store", "-output", "internal/store")
	require.NoError(t, err, output)
	require.FileExists(t, filepath.Join(dir, "internal", "store", "users_gen.go"))
}

func TestConfigFlagOverrides(t *testing.T) {
	t.Run("a typed flag wins", func(t *testing.T) {
		dir, databasePath := configModule(t, `{"package": "store", "output": "internal/store", "dialect": "sqlite"}`)

		output, err := runConfigured(t, dir, "-dsn", databasePath, "-package", "records", "-output", "internal/records")
		require.NoError(t, err, output)
		require.FileExists(t, filepath.Join(dir, "internal", "records", "users_gen.go"))
		require.NoDirExists(t, filepath.Join(dir, "internal", "store"))

		source, err := os.ReadFile(filepath.Join(dir, "internal", "records", "users_gen.go"))
		require.NoError(t, err)
		require.Contains(t, string(source), "package records")
	})

	// -prune is the flag whose default is true, so "the user typed false"
	// and "the user typed nothing" have to stay distinguishable.
	t.Run("a typed prune=false wins over the file", func(t *testing.T) {
		dir, databasePath := configModule(t, `{"package": "store", "output": "internal/store", "dialect": "sqlite", "prune": true}`)

		output, err := runConfigured(t, dir, "-dsn", databasePath)
		require.NoError(t, err, output)

		orphan := filepath.Join(dir, "internal", "store", "audit_log_gen.go")
		require.FileExists(t, orphan)

		output, err = runConfigured(t, dir, "-dsn", databasePath, "-include", "users", "-prune=false")
		require.Error(t, err, output)
		require.Contains(t, err.Error(), "audit_log_gen.go")
		require.FileExists(t, orphan, "a refused run deletes nothing")
	})

	t.Run("the file supplies what no flag names", func(t *testing.T) {
		dir, databasePath := configModule(t, `{"package": "store", "output": "internal/store", "dialect": "sqlite", "prune": false}`)

		output, err := runConfigured(t, dir, "-dsn", databasePath)
		require.NoError(t, err, output)
		output, err = runConfigured(t, dir, "-dsn", databasePath, "-include", "users")
		require.Error(t, err, output)
		require.Contains(t, err.Error(), "audit_log_gen.go", "prune:false in the file must reach the run")
	})
}

func TestConfigRefusals(t *testing.T) {
	testCases := []struct {
		name     string
		settings string
		args     []string
		expected string
	}{
		{
			// A misspelled key is the failure a settings file is worst at
			// showing, so it is named rather than ignored.
			name:     "unknown key",
			settings: `{"package": "store", "output": "internal/store", "dialect": "sqlite", "rowname": {}}`,
			expected: `unknown field "rowname"`,
		},
		{
			name:     "malformed JSON",
			settings: `{"package": "store",}`,
			expected: "parse config",
		},
		{
			name:     "trailing value",
			settings: `{"package": "store", "output": "internal/store", "dialect": "sqlite"} {"package": "other"}`,
			expected: "unexpected value after the settings object",
		},
		{
			name:     "blank row name",
			settings: `{"package": "store", "output": "internal/store", "dialect": "sqlite", "tables": {"row_names": {"users": ""}}}`,
			expected: `states an empty row name for table "users"`,
		},
		{
			name:     "query with no function",
			settings: `{"package": "store", "output": "internal/store", "dialect": "sqlite", "queries": [{"input": "queries/x.sql"}]}`,
			expected: `config query "queries/x.sql" states no function`,
		},
		{
			name:     "query with no input",
			settings: `{"package": "store", "output": "internal/store", "dialect": "sqlite", "queries": [{"function": "X"}]}`,
			expected: "config query 1 states no input",
		},
		{
			name:     "explicit config that does not exist",
			settings: `{"package": "store", "output": "internal/store", "dialect": "sqlite"}`,
			args:     []string{"-config", "absent.json"},
			expected: "read config absent.json",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			dir, databasePath := configModule(t, testCase.settings)
			args := append([]string{"-dsn", databasePath}, testCase.args...)
			_, err := runConfigured(t, dir, args...)
			require.Error(t, err)
			require.Contains(t, err.Error(), testCase.expected)
		})
	}
}

// TestConfigNamedByFlagIsRead requires -config to reach a file the default
// lookup would never find.
func TestConfigNamedByFlagIsRead(t *testing.T) {
	dir, databasePath := configModule(t, "")
	elsewhere := filepath.Join(dir, "build", "codegen.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(elsewhere), 0o755))
	require.NoError(t, os.WriteFile(elsewhere, []byte(`{"package": "store", "output": "internal/store", "dialect": "sqlite"}`), 0o600))

	output, err := runConfigured(t, dir, "-dsn", databasePath, "-config", elsewhere)
	require.NoError(t, err, output)
	require.FileExists(t, filepath.Join(dir, "internal", "store", "users_gen.go"))
}

// TestConfigRejectsADSN requires the one setting that must never be checked
// in to be refused rather than quietly accepted.
func TestConfigRejectsADSN(t *testing.T) {
	dir, databasePath := configModule(t, `{"package": "store", "output": "internal/store", "dialect": "sqlite", "dsn": "postgres://user:secret@example.test/db"}`)

	_, err := runConfigured(t, dir, "-dsn", databasePath)
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown field "dsn"`)
	require.NotContains(t, err.Error(), "secret")
}

// TestConfigDerivesQueryOutputNames pins the rule that lets a query state
// only its input and its function.
func TestConfigDerivesQueryOutputNames(t *testing.T) {
	dir, databasePath := configModule(t, `{
  "package": "store",
  "output": "internal/store",
  "dialect": "sqlite",
  "queries": [{"input": "queries/user_by_email.sql", "function": "UserByEmail"}]
}`)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "queries"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "queries", "user_by_email.sql"),
		[]byte(`SELECT id FROM users WHERE email = {{bind "email"}}`), 0o600))

	output, err := runConfigured(t, dir, "-dsn", databasePath)
	require.NoError(t, err, output)

	source, err := os.ReadFile(filepath.Join(dir, "internal", "store", "user_by_email_gen.go"))
	require.NoError(t, err)
	require.True(t, strings.Contains(string(source), "func UserByEmail("))
}
