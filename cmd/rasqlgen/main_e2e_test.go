package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGoRunSchemaGeneratesCompilableSource(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-go-run-*")
	require.NoError(t, err)
	directory, err = filepath.Abs(directory)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	consumer := filepath.Join(directory, "consumer")
	outputDirectory := filepath.Join(consumer, "internal", "store")
	require.NoError(t, os.MkdirAll(outputDirectory, 0o700))
	goMod := "module example.com/consumer\n\ngo 1.26\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(consumer, "go.mod"), []byte(goMod), 0o600))
	input := filepath.Join(consumer, "schema.json")
	output := filepath.Join(outputDirectory, "rasql_gen.go")
	data := []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":"integer"}],"PrimaryKey":["id"]}]`)
	require.NoError(t, os.WriteFile(input, data, 0o600))

	command := exec.CommandContext(t.Context(), "go", "get", "github.com/lestrrat-go/rasql/cmd/rasqlgen@v0.0.0")
	command.Dir = consumer
	commandOutput, err := command.CombinedOutput()
	require.NoError(t, err, string(commandOutput))

	command = exec.CommandContext(t.Context(), "go", "run", "github.com/lestrrat-go/rasql/cmd/rasqlgen", "schema", "-input", input, "-package", "store", "-output", output)
	command.Dir = consumer
	commandOutput, err = command.CombinedOutput()
	require.NoError(t, err, string(commandOutput))

	source, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Contains(t, string(source), "var usersTable = rasql.MustTable[UsersRow](schema.Table{")
	require.Contains(t, string(source), "func Users() rasql.Table[UsersRow] {")

	command = exec.CommandContext(t.Context(), "go", "test", "./...")
	command.Dir = consumer
	commandOutput, err = command.CombinedOutput()
	require.NoError(t, err, string(commandOutput))
}
