//go:build unix

package rasqlgen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// This test uses Unix-only symbolic links.

func TestRunSchemaWritesToOutputDirectorySymlink(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "schema.json")
	require.NoError(t, os.WriteFile(input, []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":{"Kind":"integer","Unsigned":false}}],"PrimaryKey":["id"]}]`), 0o600))
	target := filepath.Join(directory, "generated")
	require.NoError(t, os.Mkdir(target, 0o700))
	output := filepath.Join(directory, "output")
	require.NoError(t, os.Symlink("generated", output))

	require.NoError(t, run([]string{"schema", "-input", input, "-package", "generated", "-output", output}))
	require.FileExists(t, filepath.Join(target, "users_gen.go"))
}
