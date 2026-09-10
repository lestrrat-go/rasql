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

func TestSourceFileSnapshotResolvesLinkTargetsIncrementally(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "a"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "b", "deep"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a", "schema.sql"), []byte("wrong"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "b", "schema.sql"), []byte("right"), 0o600))
	require.NoError(t, os.Symlink("../b/deep", filepath.Join(root, "a", "jump")))
	require.NoError(t, os.Symlink("a/jump/../schema.sql", filepath.Join(root, "source.sql")))
	want, err := os.ReadFile(filepath.Join(root, "source.sql"))
	require.NoError(t, err)
	snapshot, err := compilerlock.SnapshotSourceFile(root, "source.sql")
	require.NoError(t, err)
	require.Equal(t, want, snapshot.Bytes())
}

func TestSourceFileSnapshotBoundsAndPaths(t *testing.T) {
	root := t.TempDir()
	for _, size := range []int{int(compilerlock.MaxSourceFileBytes), int(compilerlock.MaxSourceFileBytes + 1)} {
		name := filepath.Join(root, "source-"+string(rune('a'+size%2))+".sql")
		require.NoError(t, os.WriteFile(name, bytes.Repeat([]byte{'x'}, size), 0o600))
		_, err := compilerlock.SnapshotSourceFile(root, filepath.Base(name))
		if int64(size) == compilerlock.MaxSourceFileBytes {
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
