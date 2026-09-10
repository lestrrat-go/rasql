package sourcefile_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/sourcefile"
	"github.com/stretchr/testify/require"
)

func TestSnapshotSourceFileIdentityAndCloning(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "replacement.sql"), []byte("select 1"), 0o600))
	require.NoError(t, os.Rename(filepath.Join(root, "replacement.sql"), filepath.Join(root, "schema.sql")))
	snapshot, err := sourcefile.SnapshotSourceFile(root, "schema.sql")
	require.NoError(t, err)
	require.Equal(t, "schema.sql", snapshot.Path())
	require.Len(t, snapshot.SHA256(), 64)

	bytesCopy := snapshot.Bytes()
	bytesCopy[0] = 'X'
	require.Equal(t, []byte("select 1"), snapshot.Bytes())
	require.NoError(t, sourcefile.RevalidateSourceFiles(root, []sourcefile.SourceFileSnapshot{snapshot}))

	require.NoError(t, os.WriteFile(filepath.Join(root, "replacement.sql"), []byte("select 1"), 0o600))
	require.NoError(t, os.Rename(filepath.Join(root, "replacement.sql"), filepath.Join(root, "schema.sql")))
	require.ErrorIs(t, snapshot.Revalidate(), sourcefile.ErrSourceChanged)
}

func TestSnapshotSourceFileSHA256MatchesContentDigest(t *testing.T) {
	root := t.TempDir()
	content := []byte("create table t (id int);")
	require.NoError(t, os.WriteFile(filepath.Join(root, "schema.sql"), content, 0o600))
	snapshot, err := sourcefile.SnapshotSourceFile(root, "schema.sql")
	require.NoError(t, err)
	want := sha256.Sum256(content)
	require.Equal(t, hex.EncodeToString(want[:]), snapshot.SHA256())
}

func TestSnapshotSourceFileSymlinkChain(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "schema.sql"), []byte("select 1"), 0o600))
	require.NoError(t, os.Symlink("./schema.sql", filepath.Join(root, "one")))
	require.NoError(t, os.Symlink("one", filepath.Join(root, "two")))
	snapshot, err := sourcefile.SnapshotSourceFile(root, "two")
	require.NoError(t, err)
	require.NoError(t, snapshot.Revalidate())
	require.NoError(t, os.Remove(filepath.Join(root, "one")))
	require.NoError(t, os.Symlink("schema.sql", filepath.Join(root, "one")))
	require.ErrorIs(t, snapshot.Revalidate(), sourcefile.ErrSourceChanged)
}

func TestSnapshotSourceFileResolvesLinkTargetsIncrementally(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "a"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "b", "deep"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a", "schema.sql"), []byte("wrong"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "b", "schema.sql"), []byte("right"), 0o600))
	require.NoError(t, os.Symlink("../b/deep", filepath.Join(root, "a", "jump")))
	require.NoError(t, os.Symlink("a/jump/../schema.sql", filepath.Join(root, "source.sql")))
	want, err := os.ReadFile(filepath.Join(root, "source.sql"))
	require.NoError(t, err)
	snapshot, err := sourcefile.SnapshotSourceFile(root, "source.sql")
	require.NoError(t, err)
	require.Equal(t, want, snapshot.Bytes())
}

func TestSnapshotSourceFileBoundsAndPaths(t *testing.T) {
	root := t.TempDir()
	for _, size := range []int{int(sourcefile.MaxSourceFileBytes), int(sourcefile.MaxSourceFileBytes + 1)} {
		name := filepath.Join(root, "source-"+string(rune('a'+size%2))+".sql")
		require.NoError(t, os.WriteFile(name, bytes.Repeat([]byte{'x'}, size), 0o600))
		_, err := sourcefile.SnapshotSourceFile(root, filepath.Base(name))
		if int64(size) == sourcefile.MaxSourceFileBytes {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
	_, err := sourcefile.SnapshotSourceFile(root, ".")
	require.Error(t, err)
	_, err = sourcefile.SnapshotSourceFile("relative", "source.sql")
	require.Error(t, err)
	require.False(t, errors.Is(err, sourcefile.ErrSourceChanged))
}

func TestSnapshotSourceFileRejectsPathEscape(t *testing.T) {
	root := t.TempDir()
	_, err := sourcefile.SnapshotSourceFile(root, "../outside.sql")
	require.Error(t, err)
	_, err = sourcefile.NormalizePath("../outside.sql")
	require.Error(t, err)
}

func TestSourceBytesRefusesMissingFile(t *testing.T) {
	root := t.TempDir()
	_, err := sourcefile.SourceBytes(filepath.Join(root, "missing"))
	require.Error(t, err)
}
