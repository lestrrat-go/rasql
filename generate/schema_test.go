package generate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestSchemaIsDeterministicAndCompiles(t *testing.T) {
	users := schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText, Nullable: true},
		},
		PrimaryKey: []string{"id"},
	}
	orders := schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	}

	source, err := generate.Schema("generated", users, orders)
	require.NoError(t, err)
	require.Contains(t, string(source), "var Orders = schema.Table")
	require.Contains(t, string(source), "var Users = schema.Table")
	require.Less(t, stringIndex(t, source, "var Orders"), stringIndex(t, source, "var Users"))

	directory, err := os.MkdirTemp(".", ".tmp-schema-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema.go"), source, 0o600))
	module := "module example.com/generated\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => ../..\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte(module), 0o600))

	command := exec.CommandContext(t.Context(), "go", "test", ".")
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "go test output:\n%s", output)
}

func TestSchemaRejectsInvalidPackageName(t *testing.T) {
	_, err := generate.Schema("not-valid")
	require.Error(t, err)
}

func stringIndex(t *testing.T, source []byte, value string) int {
	t.Helper()
	index := len(source)
	for offset := 0; offset+len(value) <= len(source); offset++ {
		if string(source[offset:offset+len(value)]) == value {
			index = offset
			break
		}
	}
	require.NotEqual(t, len(source), index)
	return index
}
