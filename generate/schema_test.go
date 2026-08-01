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
			{Name: "created_at", Type: schema.TypeTime},
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
	require.Contains(t, string(source), "type OrdersRow struct")
	require.Contains(t, string(source), "type UsersRow struct")
	require.Contains(t, string(source), "Email")
	require.Contains(t, string(source), "CreatedAt")
	require.Contains(t, string(source), "`rasql:\"email\"`")
	require.Contains(t, string(source), "`rasql:\"created_at\"`")
	require.Contains(t, string(source), "var Orders = rasql.MustTable[OrdersRow](schema.Table{")
	require.Contains(t, string(source), "var Users = rasql.MustTable[UsersRow](schema.Table{")
	require.Less(t, stringIndex(t, source, "var Orders"), stringIndex(t, source, "var Users"))

	directory, err := os.MkdirTemp(".", ".tmp-schema-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema.go"), source, 0o600))
	module := "module example.com/generated\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => ../..\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte(module), 0o600))

	command := exec.CommandContext(t.Context(), "go", "mod", "tidy")
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "go mod tidy output:\n%s", output)

	command = exec.CommandContext(t.Context(), "go", "test", ".")
	command.Dir = directory
	output, err = command.CombinedOutput()
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
