package rasqlgen

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/gensum"
	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// liveWorkflowFixture is a small, real project for the new database-backed generate/check path:
// a go.mod (so modroot.From finds a module root), one migration directory with two migrations,
// and one file-backed query. Every test in this file runs it through -scratch, which builds and
// drops a real SQLite database, applies both migrations for real, reads the real catalog back,
// and compiles a real store package -- a real producer's output, not a fixture standing in for
// one.
type liveWorkflowFixture struct {
	root, configPath, queryPath, migration2Path string
}

func newLiveWorkflowFixture(t *testing.T) liveWorkflowFixture {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/livefixture\n\ngo 1.24\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "migrations", "001_init"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "migrations", "001_init", "1.up.sql"), []byte("CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL);\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "migrations", "002_orders"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "migrations", "002_orders", "1.up.sql"), []byte("CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL);\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "queries"), 0o700))
	query := "SELECT id, email FROM users WHERE id = {{bind \"id\" users.id}}\n"
	queryPath := filepath.Join(root, "queries", "user_by_id.sql")
	require.NoError(t, os.WriteFile(queryPath, []byte(query), 0o600))
	config := map[string]any{
		"dialect": "sqlite", "migrations": "migrations",
		"package": "store", "output": "internal/store", "emitter": "compact",
		"queries": []any{map[string]any{
			"id": "user_by_id", "input": "queries/user_by_id.sql", "engine": "sqlite", "function": "UserByID", "output": "user_by_id_gen.go",
			"operation": "select", "cardinality": "one",
			"parameters": []any{map[string]any{"name": "id", "scalar": "integer", "nullable": false}},
			"results": []any{
				map[string]any{"name": "id", "scalar": "integer", "nullable": false},
				map[string]any{"name": "email", "scalar": "text", "nullable": false},
			},
		}},
	}
	configBytes, err := json.Marshal(config)
	require.NoError(t, err)
	configPath := filepath.Join(root, "rasql.json")
	require.NoError(t, os.WriteFile(configPath, configBytes, 0o600))
	return liveWorkflowFixture{root: root, configPath: configPath, queryPath: queryPath, migration2Path: filepath.Join(root, "migrations", "002_orders", "1.up.sql")}
}

func (f liveWorkflowFixture) command(t *testing.T, output, diagnostics *bytes.Buffer) *command {
	t.Helper()
	return &command{program: "rasql", output: output, diagnostics: diagnostics, ctx: t.Context()}
}

func (f liveWorkflowFixture) generatedFiles(t *testing.T) map[string][]byte {
	t.Helper()
	dir := filepath.Join(f.root, "internal", "store")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	files := make(map[string][]byte)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, readErr)
		files[entry.Name()] = data
	}
	return files
}

func TestGenerateScratchSQLiteWritesStoreAndSum(t *testing.T) {
	fixture := newLiveWorkflowFixture(t)
	var output, diagnostics bytes.Buffer
	command := fixture.command(t, &output, &diagnostics)

	require.NoError(t, command.run([]string{"generate", "-config", fixture.configPath, "-scratch"}), diagnostics.String())

	files := fixture.generatedFiles(t)
	require.Contains(t, files, "users_gen.go")
	require.Contains(t, files, "orders_gen.go")
	require.Contains(t, files, "user_by_id_gen.go")
	require.Contains(t, files, "schema_gen.go")
	require.Contains(t, files, "schema_gen_test.go")
	require.Contains(t, string(files["user_by_id_gen.go"]), "func UserByID(id int64)")
	// The migration history table itself, and its companions, are never generated.
	require.NotContains(t, files, "rasql_schema_migrations_gen.go")

	sumBytes, err := os.ReadFile(filepath.Join(fixture.root, "internal", "store", "rasql.sum"))
	require.NoError(t, err)
	sum, err := gensum.Parse(sumBytes)
	require.NoError(t, err)
	require.Equal(t, "sqlite", sum.Dialect)
	require.NotEmpty(t, sum.Profile)
	require.NotEmpty(t, sum.Settings)
	require.Len(t, sum.Migrations, 2)
	require.Equal(t, "001_init", sum.Migrations[0].Name)
	require.Equal(t, "002_orders", sum.Migrations[1].Name)
	require.Len(t, sum.Queries, 1)
	require.Equal(t, "queries/user_by_id.sql", sum.Queries[0].Name)
	require.NotEmpty(t, sum.Outputs)
	for _, entry := range sum.Outputs {
		require.NotEqual(t, "rasql.sum", entry.Name)
	}
}

func TestGenerateScratchIsByteIdenticalOnRerun(t *testing.T) {
	fixture := newLiveWorkflowFixture(t)
	var output, diagnostics bytes.Buffer
	command := fixture.command(t, &output, &diagnostics)
	require.NoError(t, command.run([]string{"generate", "-config", fixture.configPath, "-scratch"}), diagnostics.String())
	before := fixture.generatedFiles(t)

	output.Reset()
	diagnostics.Reset()
	require.NoError(t, command.run([]string{"generate", "-config", fixture.configPath, "-scratch"}), diagnostics.String())
	require.Equal(t, before, fixture.generatedFiles(t))
}

func TestCheckOfflinePassesAfterGenerate(t *testing.T) {
	fixture := newLiveWorkflowFixture(t)
	var output, diagnostics bytes.Buffer
	command := fixture.command(t, &output, &diagnostics)
	require.NoError(t, command.run([]string{"generate", "-config", fixture.configPath, "-scratch"}), diagnostics.String())
	before := fixture.generatedFiles(t)

	output.Reset()
	diagnostics.Reset()
	require.NoError(t, command.run([]string{"check", "-config", fixture.configPath}), diagnostics.String())
	require.Contains(t, output.String(), "no database was consulted")
	require.Equal(t, before, fixture.generatedFiles(t))
}

func TestCheckOfflineSaysNoDatabaseWasConsulted(t *testing.T) {
	fixture := newLiveWorkflowFixture(t)
	var output, diagnostics bytes.Buffer
	command := fixture.command(t, &output, &diagnostics)
	require.NoError(t, command.run([]string{"generate", "-config", fixture.configPath, "-scratch"}), diagnostics.String())

	require.NoError(t, os.WriteFile(fixture.queryPath, []byte("SELECT id, email FROM users WHERE id = {{bind \"id\" users.id}} OR 1=1\n"), 0o600))
	output.Reset()
	diagnostics.Reset()
	err := command.run([]string{"check", "-config", fixture.configPath})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no database was consulted")
}

func TestCheckOfflineRefusesMissingSum(t *testing.T) {
	fixture := newLiveWorkflowFixture(t)
	var output, diagnostics bytes.Buffer
	command := fixture.command(t, &output, &diagnostics)
	err := command.run([]string{"check", "-config", fixture.configPath})
	require.Error(t, err)
	require.Contains(t, err.Error(), "rasql codegen generate")
	require.ErrorIs(t, err, generate.ErrStale)
}

func TestCheckOfflineNamesMigrationsGroup(t *testing.T) {
	fixture := newLiveWorkflowFixture(t)
	var output, diagnostics bytes.Buffer
	command := fixture.command(t, &output, &diagnostics)
	require.NoError(t, command.run([]string{"generate", "-config", fixture.configPath, "-scratch"}), diagnostics.String())
	before := fixture.generatedFiles(t)

	require.NoError(t, os.WriteFile(fixture.migration2Path, []byte("CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, total INTEGER NOT NULL);\n"), 0o600))
	output.Reset()
	diagnostics.Reset()
	err := command.run([]string{"check", "-config", fixture.configPath})
	require.Error(t, err)
	require.ErrorIs(t, err, generate.ErrStale)
	require.Contains(t, err.Error(), "migrations")
	require.Equal(t, before, fixture.generatedFiles(t))
}

func TestCheckOfflineNamesQueriesGroup(t *testing.T) {
	fixture := newLiveWorkflowFixture(t)
	var output, diagnostics bytes.Buffer
	command := fixture.command(t, &output, &diagnostics)
	require.NoError(t, command.run([]string{"generate", "-config", fixture.configPath, "-scratch"}), diagnostics.String())
	before := fixture.generatedFiles(t)

	require.NoError(t, os.WriteFile(fixture.queryPath, []byte("SELECT id, email FROM users WHERE id = {{bind \"id\" users.id}} OR 1=1\n"), 0o600))
	output.Reset()
	diagnostics.Reset()
	err := command.run([]string{"check", "-config", fixture.configPath})
	require.Error(t, err)
	require.ErrorIs(t, err, generate.ErrStale)
	require.Contains(t, err.Error(), "queries")
	require.Equal(t, before, fixture.generatedFiles(t))
}

func TestCheckOfflineNamesOutputsGroup(t *testing.T) {
	fixture := newLiveWorkflowFixture(t)
	var output, diagnostics bytes.Buffer
	command := fixture.command(t, &output, &diagnostics)
	require.NoError(t, command.run([]string{"generate", "-config", fixture.configPath, "-scratch"}), diagnostics.String())
	before := fixture.generatedFiles(t)

	generatedPath := filepath.Join(fixture.root, "internal", "store", "users_gen.go")
	generatedBytes, err := os.ReadFile(generatedPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(generatedPath, append(generatedBytes, []byte("\n// hand-edited\n")...), 0o600))
	diagnostics.Reset()
	checkErr := command.run([]string{"check", "-config", fixture.configPath})
	require.Error(t, checkErr)
	require.ErrorIs(t, checkErr, generate.ErrStale)
	require.Contains(t, checkErr.Error(), "outputs")
	require.Equal(t, before["users_gen.go"], generatedBytes)
	require.Equal(t, append(generatedBytes, []byte("\n// hand-edited\n")...), workflowRead(t, generatedPath))
}

func TestCheckOfflineNamesSettingsGroup(t *testing.T) {
	fixture := newLiveWorkflowFixture(t)
	var output, diagnostics bytes.Buffer
	command := fixture.command(t, &output, &diagnostics)
	require.NoError(t, command.run([]string{"generate", "-config", fixture.configPath, "-scratch"}), diagnostics.String())
	before := fixture.generatedFiles(t)

	configBytes, err := os.ReadFile(fixture.configPath)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(configBytes, &raw))
	raw["prune"] = false
	updated, err := json.Marshal(raw)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(fixture.configPath, updated, 0o600))

	output.Reset()
	diagnostics.Reset()
	checkErr := command.run([]string{"check", "-config", fixture.configPath})
	require.Error(t, checkErr)
	require.ErrorIs(t, checkErr, generate.ErrStale)
	require.Contains(t, checkErr.Error(), "settings")
	require.Equal(t, before, fixture.generatedFiles(t))
}

// TestGenerateRefusesPendingMigrationOnSQLiteNonScratch is the SQLite twin of the live
// TestGenerateRefusesPendingMigration: a persistent (non-scratch) SQLite database that has had
// only its first migration applied refuses generate -dsn, names the pending migration, and writes
// nothing.
func TestGenerateRefusesPendingMigrationOnSQLiteNonScratch(t *testing.T) {
	fixture := newLiveWorkflowFixture(t)
	dbPath := filepath.Join(fixture.root, "app.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	runner, err := migrate.New(db, dialect.SQLite())
	require.NoError(t, err)
	migrations, err := migrationdir.Load(filepath.Join(fixture.root, "migrations"))
	require.NoError(t, err)
	require.Len(t, migrations, 2)
	_, err = runner.Apply(t.Context(), migrate.AllPending(), migrations[0])
	require.NoError(t, err)

	var output, diagnostics bytes.Buffer
	command := fixture.command(t, &output, &diagnostics)
	err = command.run([]string{"generate", "-config", fixture.configPath, "-dsn", dbPath})
	require.Error(t, err)
	require.Contains(t, err.Error(), "002_orders")
	require.Contains(t, err.Error(), "rasql migrate apply -dir migrations")
	require.NoDirExists(t, filepath.Join(fixture.root, "internal", "store"))
}
