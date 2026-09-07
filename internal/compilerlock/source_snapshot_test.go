package compilerlock_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/stretchr/testify/require"
)

func TestSourceFileSnapshotIdentityAndCloning(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "replacement.sql"), []byte("select 1"), 0o600))
	require.NoError(t, os.Rename(filepath.Join(root, "replacement.sql"), filepath.Join(root, "schema.sql")))
	snapshot, err := compilerlock.SnapshotSourceFile(root, "schema.sql")
	require.NoError(t, err)
	require.Equal(t, "schema.sql", snapshot.Path())
	require.Equal(t, "schema.sql", snapshot.Record().Path)
	require.Len(t, snapshot.Record().SHA256, 64)

	bytesCopy := snapshot.Bytes()
	bytesCopy[0] = 'X'
	require.Equal(t, []byte("select 1"), snapshot.Bytes())
	require.NoError(t, compilerlock.RevalidateSourceFiles(root, []compilerlock.SourceFileSnapshot{snapshot}))

	require.NoError(t, os.WriteFile(filepath.Join(root, "replacement.sql"), []byte("select 1"), 0o600))
	require.NoError(t, os.Rename(filepath.Join(root, "replacement.sql"), filepath.Join(root, "schema.sql")))
	require.ErrorIs(t, snapshot.Revalidate(), compilerlock.ErrSourceChanged)
}

func TestSourceFileSnapshotSymlinkChain(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "schema.sql"), []byte("select 1"), 0o600))
	require.NoError(t, os.Symlink("./schema.sql", filepath.Join(root, "one")))
	require.NoError(t, os.Symlink("one", filepath.Join(root, "two")))
	snapshot, err := compilerlock.SnapshotSourceFile(root, "two")
	require.NoError(t, err)
	require.NoError(t, snapshot.Revalidate())
	require.NoError(t, os.Remove(filepath.Join(root, "one")))
	require.NoError(t, os.Symlink("schema.sql", filepath.Join(root, "one")))
	require.ErrorIs(t, snapshot.Revalidate(), compilerlock.ErrSourceChanged)
}

func TestSourceFileSnapshotBoundsAndPaths(t *testing.T) {
	root := t.TempDir()
	for _, size := range []int{compilerlock.MaxSourceFileBytes, compilerlock.MaxSourceFileBytes + 1} {
		name := filepath.Join(root, "source-"+string(rune('a'+size%2))+".sql")
		require.NoError(t, os.WriteFile(name, bytes.Repeat([]byte{'x'}, size), 0o600))
		_, err := compilerlock.SnapshotSourceFile(root, filepath.Base(name))
		if size == compilerlock.MaxSourceFileBytes {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
	_, err := compilerlock.SnapshotSourceFile(root, ".")
	require.Error(t, err)
	_, err = compilerlock.SnapshotSourceFile("relative", "source.sql")
	require.Error(t, err)
	require.False(t, errors.Is(err, compilerlock.ErrSourceChanged))
}
