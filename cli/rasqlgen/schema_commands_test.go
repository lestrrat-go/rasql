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

func TestSQLiteSchemaUpdateThenOfflineGenerateAndCheck(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "migrations"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "migrations", "001_init.sql"), []byte("CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL);\n"), 0o600))
	configPath := filepath.Join(root, "rasql.json")
	config := map[string]any{
		"engine":  map[string]string{"dialect": "sqlite", "profile": "sqlite-3.35"},
		"schema":  map[string]any{"kind": "migrations", "identity": "fixture-v1", "paths": []string{"migrations/*.sql"}},
		"package": "store", "output": "internal/store", "emitter": "legacy",
	}
	configBytes, err := json.Marshal(config)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configBytes, 0o600))

	var output, diagnostics bytes.Buffer
	err = rasqlgen.RunTopLevel([]string{"schema", "update", "-config", configPath}, &output, &diagnostics)
	require.NoError(t, err, diagnostics.String())
	require.FileExists(t, filepath.Join(root, "rasql.lock.json"))
	require.FileExists(t, filepath.Join(root, "internal", "store", "users_gen.go"))
	require.NoFileExists(t, filepath.Join(root, ".rasql-update.pending.json"))

	output.Reset()
	diagnostics.Reset()
	err = rasqlgen.RunTopLevel([]string{"generate", "-config", configPath}, &output, &diagnostics)
	require.NoError(t, err, diagnostics.String())
	output.Reset()
	diagnostics.Reset()
	err = rasqlgen.RunTopLevel([]string{"check", "-config", configPath}, &output, &diagnostics)
	require.NoError(t, err, diagnostics.String())
}

func TestOfflineCheckReportsSourceDriftAndGenerateRefreshesGenerationOnly(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "migrations"), 0o755))
	migration := filepath.Join(root, "migrations", "001_init.sql")
	require.NoError(t, os.WriteFile(migration, []byte("CREATE TABLE users (id INTEGER PRIMARY KEY);\n"), 0o600))
	configPath := filepath.Join(root, "rasql.json")
	writeSchemaConfig(t, configPath, "store", "fixture-v1")

	var output, diagnostics bytes.Buffer
	require.NoError(t, rasqlgen.RunTopLevel([]string{"schema", "update", "-config", configPath}, &output, &diagnostics), diagnostics.String())
	lockBefore := readFile(t, filepath.Join(root, "rasql.lock.json"))

	require.NoError(t, os.WriteFile(migration, []byte("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);\n"), 0o600))
	output.Reset()
	diagnostics.Reset()
	err := rasqlgen.RunTopLevel([]string{"check", "-config", configPath}, &output, &diagnostics)
	require.Error(t, err)
	require.ErrorIs(t, err, generate.ErrStale)
	require.Contains(t, err.Error(), "source")
	require.Equal(t, lockBefore, readFile(t, filepath.Join(root, "rasql.lock.json")))

	require.NoError(t, os.WriteFile(migration, []byte("CREATE TABLE users (id INTEGER PRIMARY KEY);\n"), 0o600))
	writeSchemaConfig(t, configPath, "renamed", "fixture-v1")
	output.Reset()
	diagnostics.Reset()
	require.NoError(t, rasqlgen.RunTopLevel([]string{"generate", "-config", configPath}, &output, &diagnostics), diagnostics.String())
	lockAfter := readFile(t, filepath.Join(root, "rasql.lock.json"))
	require.NotEqual(t, lockBefore, lockAfter)
}

func TestSchemaUpdatePropagatesCancellationBeforePublication(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "migrations"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "migrations", "001_init.sql"), []byte("CREATE TABLE users (id INTEGER PRIMARY KEY);\n"), 0o600))
	configPath := filepath.Join(root, "rasql.json")
	writeSchemaConfig(t, configPath, "store", "fixture-cancel")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output, diagnostics bytes.Buffer
	err := rasqlgen.RunTopLevelContext(ctx, []string{"schema", "update", "-config", configPath}, &output, &diagnostics)
	require.ErrorIs(t, err, context.Canceled)
	require.NoFileExists(t, filepath.Join(root, ".rasql-update.pending.json"))
	require.NoFileExists(t, filepath.Join(root, "rasql.lock.json"))
}

func writeSchemaConfig(t *testing.T, path, packageName, identity string) {
	t.Helper()
	config := map[string]any{
		"engine":  map[string]string{"dialect": "sqlite", "profile": "sqlite-3.35"},
		"schema":  map[string]any{"kind": "migrations", "identity": identity, "paths": []string{"migrations/*.sql"}},
		"package": packageName, "output": "internal/store", "emitter": "legacy",
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
