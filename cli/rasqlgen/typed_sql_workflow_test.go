package rasqlgen_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestTypedSQLSchemaUpdateOfflineParity(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "queries"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "queries", "events.sql"), []byte("SELECT id, amount, occurred_at, note FROM events\n"), 0o600))
	config := map[string]any{
		"engine":  map[string]string{"dialect": "sqlite", "profile": "sqlite-3.35"},
		"schema":  map[string]any{"kind": "live", "identity": "typed-workflow"},
		"package": "store", "output": "internal/store", "emitter": "legacy",
		"queries": []any{map[string]any{"id": "events_since", "input": "queries/events.sql", "engine": "sqlite", "function": "EventsSince", "output": "events_query_gen.go", "operation": "select", "cardinality": "many", "parameters": []any{}, "results": []any{map[string]any{"name": "id", "scalar": "integer", "nullable": false}, map[string]any{"name": "amount", "scalar": "integer", "nullable": false}, map[string]any{"name": "occurred_at", "scalar": "time", "nullable": false}, map[string]any{"name": "note", "scalar": "text", "nullable": true}}}},
	}
	configBytes, err := json.Marshal(config)
	require.NoError(t, err)
	configPath := filepath.Join(root, "rasql.json")
	require.NoError(t, os.WriteFile(configPath, configBytes, 0o600))
	dsn := filepath.Join(root, "schema.db")
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE events (id INTEGER PRIMARY KEY, amount INTEGER NOT NULL, occurred_at DATETIME NOT NULL, note TEXT NULL); INSERT INTO events VALUES (1, 4, '2024-01-01T00:00:00Z', NULL)")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	t.Chdir(root)
	var out, diag bytes.Buffer
	require.NoError(t, rasqlgen.RunTopLevel([]string{"schema", "update", "-config", configPath, "-dsn", dsn}, &out, &diag), diag.String())
	online := snapshotGenerated(t, root)
	lock := mustRead(t, filepath.Join(root, "rasql.lock.json"))
	require.NoError(t, os.Remove(filepath.Join(root, "internal", "store", "events_query_gen.go")))
	out.Reset()
	diag.Reset()
	require.NoError(t, rasqlgen.RunTopLevel([]string{"generate", "-config", configPath}, &out, &diag), diag.String())
	require.Equal(t, online, snapshotGenerated(t, root))
	require.Equal(t, lock, mustRead(t, filepath.Join(root, "rasql.lock.json")))
	out.Reset()
	diag.Reset()
	require.NoError(t, rasqlgen.RunTopLevel([]string{"check", "-config", configPath}, &out, &diag), diag.String())
}

func snapshotGenerated(t *testing.T, root string) []byte {
	t.Helper()
	return mustRead(t, filepath.Join(root, "internal", "store", "events_query_gen.go"))
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
