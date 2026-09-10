package conformance

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	"github.com/lestrrat-go/rasql/internal/scratchmod"
	"github.com/stretchr/testify/require"
)

// TestGeneratedFromDatabaseStoreCompilesAndRunsTests is the compiled-and-run proof the new,
// database-backed generation path owes this campaign (decision 12): codegen generate -check and
// check -dsn already proved that check accepts what the real generator writes, but neither
// compiled the result as its own module and ran a query against it the way
// cli/rasqlgen/compact_workflow_acceptance_test.go does for the offline, lock-backed path. Here,
// generate -scratch builds a throwaway SQLite database -- itself a real database, not a fixture --
// applies one migration to it, and writes the store; a fresh module then imports that store, and
// go test running against it is what actually fails if rasqlgen ever emits Go that does not
// compile, rather than that surfacing only once a reader tries to build it.
func TestGeneratedFromDatabaseStoreCompilesAndRunsTests(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "migrations", "001_init"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "migrations", "001_init", "001.up.sql"),
		[]byte("CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT NOT NULL);\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "queries"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "queries", "widget_by_id.sql"),
		[]byte("SELECT id, name FROM widgets WHERE id = {{bind \"id\" widgets.id}}\n"), 0o600))
	config := map[string]any{
		"dialect": "sqlite", "migrations": "migrations", "package": "store", "output": "internal/store", "emitter": "compact",
		"queries": []any{map[string]any{
			"id": "widget_by_id", "input": "queries/widget_by_id.sql", "engine": "sqlite",
			"function": "WidgetByID", "output": "widget_by_id_gen.go", "operation": "select", "cardinality": "one",
			"parameters": []any{map[string]any{"name": "id", "scalar": "integer", "nullable": false}},
			"results": []any{
				map[string]any{"name": "id", "scalar": "integer", "nullable": false},
				map[string]any{"name": "name", "scalar": "text", "nullable": false},
			},
		}},
	}
	configBytes, err := json.Marshal(config)
	require.NoError(t, err)
	configPath := filepath.Join(root, "rasql.json")
	require.NoError(t, os.WriteFile(configPath, configBytes, 0o600))

	var output, diagnostics bytes.Buffer
	err = rasqlgen.RunContext(t.Context(), []string{"generate", "-config", configPath, "-scratch"}, &output, &diagnostics)
	require.NoError(t, err, diagnostics.String())
	require.FileExists(t, filepath.Join(root, "internal", "store", "rasql.sum"))
	require.FileExists(t, filepath.Join(root, "internal", "store", "widget_by_id_gen.go"))

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	require.NoError(t, scratchmod.Write(root, repoRoot, "example.test/database-generated"))
	consumerDir := filepath.Join(root, "consumer")
	require.NoError(t, os.MkdirAll(consumerDir, 0o700))
	const source = `package consumer_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	store "example.test/database-generated/internal/store"
	_ "modernc.org/sqlite"
)

func TestGeneratedFromDatabaseQueryExecutes(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil { t.Fatal(err) }
	defer db.Close()
	if _, err = db.ExecContext(t.Context(), "CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT NOT NULL); INSERT INTO widgets VALUES (7, 'sprocket')"); err != nil { t.Fatal(err) }
	rdb, err := rasql.New(db, dialect.SQLite())
	if err != nil { t.Fatal(err) }
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 1)
	if err != nil { t.Fatal(err) }
	executor, err := rasql.AsExecutor(rdb, profile)
	if err != nil { t.Fatal(err) }
	query, err := store.WidgetByID(7)
	if err != nil { t.Fatal(err) }
	row, err := rasql.One(t.Context(), executor, query)
	if err != nil { t.Fatal(err) }
	if row.ID != 7 || row.Name != "sprocket" { t.Fatalf("row = %#v", row) }
}
`
	require.NoError(t, os.WriteFile(filepath.Join(consumerDir, "consumer_test.go"), []byte(source), 0o600))

	command := exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "./consumer")
	command.Dir = root
	command.Env = append(os.Environ(), "GOFLAGS=-buildvcs=false")
	commandOutput, err := command.CombinedOutput()
	require.NoError(t, err, "%s", commandOutput)
}
