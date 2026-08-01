package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGoRunSchemaGeneratesSource(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-go-run-*")
	require.NoError(t, err)
	directory, err = filepath.Abs(directory)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	input := filepath.Join(directory, "schema.json")
	output := filepath.Join(directory, "schema.go")
	data := []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":"integer"}],"PrimaryKey":["id"]}]`)
	require.NoError(t, os.WriteFile(input, data, 0o600))

	command := exec.CommandContext(t.Context(), "go", "run", "./cmd/rasqlgen", "schema", "-input", input, "-package", "generated", "-output", output)
	command.Dir = filepath.Join("..", "..")
	commandOutput, err := command.CombinedOutput()
	require.NoError(t, err, string(commandOutput))

	source, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Contains(t, string(source), "var Users = rasql.MustTable[UsersRow](schema.Table{")
}
