package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunSchemaGeneratesSource(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	input := filepath.Join(directory, "schema.json")
	output := filepath.Join(directory, "schema.go")
	data := []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":"integer"}],"PrimaryKey":["id"]}]`)
	require.NoError(t, os.WriteFile(input, data, 0o600))

	require.NoError(t, run([]string{"schema", "-input", input, "-package", "generated", "-output", output}))
	source, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Contains(t, string(source), "var Users = schema.Table")
}

func TestRunQueryGeneratesSource(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-query-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	input := filepath.Join(directory, "user.sql")
	output := filepath.Join(directory, "query.go")
	require.NoError(t, os.WriteFile(input, []byte("SELECT id FROM users WHERE id = {{bind \"id\"}}"), 0o600))

	require.NoError(t, run([]string{"query", "-input", input, "-function", "UserByID", "-dialect", "postgresql", "-package", "generated", "-output", output}))
	source, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Contains(t, string(source), "func UserByID(id any)")
	require.Contains(t, string(source), "id = $1")
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	require.Error(t, run([]string{"unknown"}))
}
