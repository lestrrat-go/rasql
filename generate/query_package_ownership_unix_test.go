//go:build unix

package generate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/stretchr/testify/require"
)

func TestQueryPackageOwnershipUsesAuthorizedDirectoryHandle(t *testing.T) {
	base := t.TempDir()
	authorized := filepath.Join(base, "authorized")
	swapped := filepath.Join(base, "swapped")
	dir := filepath.Join(base, "store")
	require.NoError(t, os.MkdirAll(authorized, 0o700))
	require.NoError(t, os.MkdirAll(swapped, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(authorized, "old_gen.go"), []byte(genfile.Marker+"\n\npackage store\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(swapped, "foreign.go"), []byte("package other\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(swapped, "swapped_gen.go"), []byte(genfile.Marker+"\n\npackage store\n"), 0o600))
	require.NoError(t, os.Symlink(authorized, dir))

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	require.NoError(t, os.Remove(dir))
	require.NoError(t, os.Symlink(swapped, dir))

	orphans, _, err := queryPackageOwnershipAt(root, dir, "store", []File{{Path: filepath.Join(dir, "query_gen.go")}}, []string{"Query"})
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(dir, "old_gen.go")}, orphans)
	require.NotContains(t, orphans, filepath.Join(dir, "swapped_gen.go"))
}
