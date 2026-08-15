// This file is package generate, not generate_test, because
// TestPlanCheckNeverReachesTheWriteSeams swaps writeGeneratedFile and
// removeGeneratedFile for stand-ins that fail the test if they are ever
// called. Both are unexported package-level seams, so only a test inside
// this package can reach one, the same way store_commit_test.go does for
// the write order. The helper table definitions it uses are that file's,
// which is the same package.
package generate

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"testing/iotest"

	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestPlanCheckNeverReachesTheWriteSeams pins Check's read-only promise on
// the implementation rather than on what a directory looks like afterwards:
// every write and every deletion Commit makes goes through one of these two
// seams, so a Check that reached either one fails here even if what it
// wrote happened to match what was already on disk.
//
// It runs Check down each of its four paths -- a clean package, a leftover
// with Prune unset, the same leftover with Prune set, and an output
// directory that does not exist -- because the seams are only reachable
// from the last two in Commit, and a directory that does not exist is where
// Commit's own step 1 creates the most.
func TestPlanCheckNeverReachesTheWriteSeams(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "store")
	users := commitTestUsersDef()
	store := Store{Package: "store", Dir: dir, Tables: []schema.TableDef{users}}
	// The setup writes run before the swap, since Write goes through the
	// very seams the swap makes fatal.
	require.NoError(t, store.Write())
	orphan := filepath.Join(dir, "dropped_gen.go")
	require.NoError(t, os.WriteFile(orphan, []byte(genfile.Marker+"\n\npackage store\n"), 0o600))

	previousWrite, previousRemove := writeGeneratedFile, removeGeneratedFile
	t.Cleanup(func() { writeGeneratedFile, removeGeneratedFile = previousWrite, previousRemove })
	writeGeneratedFile = func(_ *os.Root, name string, _ []byte) error {
		t.Errorf("Check wrote %s; it must write nothing", name)
		return nil
	}
	removeGeneratedFile = func(_ *os.Root, name string) error {
		t.Errorf("Check deleted %s; it must delete nothing", name)
		return nil
	}

	// A leftover with Prune unset: the refusal Commit gives.
	require.Error(t, store.Check())

	// The same leftover with Prune set: staleness, and the one path that
	// has a deletion in front of it.
	pruning := store
	pruning.Prune = true
	require.Error(t, pruning.Check())

	// A directory that does not exist: every planned file missing.
	missing := filepath.Join(base, "missing", "store")
	fresh := Store{Package: "store", Dir: missing, Tables: []schema.TableDef{users}}
	require.Error(t, fresh.Check())
	require.NoDirExists(t, missing)

	// A clean package, which is the path that reports nothing at all.
	require.NoError(t, os.Remove(orphan))
	require.NoError(t, store.Check())
}

// countingReader counts the bytes a reader actually handed over, which is
// what the bound below is stated in: a comparison that stops as soon as the
// answer is settled has taken no more than the planned file plus the one
// byte that says the destination goes on past it.
type countingReader struct {
	inner io.Reader
	read  int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.inner.Read(p)
	c.read += n
	return n, err
}

// endlessReader never ends. It stands in for a destination whose size is
// whatever something else left it at: a comparison that read to the end of
// this one would never return at all, so the test fails by hanging if the
// bound is ever dropped, and fails on the counted bytes if it is merely
// loosened.
type endlessReader struct{}

func (endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

// TestReaderHoldsExactlyReadsNoMoreThanWhatItComparesAgainst pins the bound
// Plan.Check reads a destination under. Only a file of the planned Source's
// own length can equal it, so the comparison reads that length and one byte
// more -- the byte whose arrival says the destination goes on past the
// planned file -- and never asks how long the destination is. Without the
// bound a destination's own size decides how much memory a check takes, and
// nothing about a path in an output directory promises a size.
func TestReaderHoldsExactlyReadsNoMoreThanWhatItComparesAgainst(t *testing.T) {
	want := []byte(genfile.Marker + "\n\npackage store\n")

	for _, tc := range []struct {
		name    string
		reader  io.Reader
		matches bool
	}{
		{name: "the same bytes and nothing after them", reader: bytes.NewReader(want), matches: true},
		{name: "the same bytes and one byte after them", reader: bytes.NewReader(append(append([]byte(nil), want...), 'x'))},
		{name: "the same bytes and a great deal after them", reader: io.MultiReader(bytes.NewReader(want), endlessReader{})},
		{name: "nothing but bytes after them", reader: endlessReader{}},
		{name: "one byte short", reader: bytes.NewReader(want[:len(want)-1])},
		{name: "empty", reader: bytes.NewReader(nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counted := &countingReader{inner: tc.reader}
			matches, err := readerHoldsExactly(counted, want)
			require.NoError(t, err)
			require.Equal(t, tc.matches, matches)
			require.LessOrEqual(t, counted.read, len(want)+1, "a comparison must read no more than what it compares against, plus the one byte that ends it")
		})
	}

	t.Run("a destination that cannot be read reports the error", func(t *testing.T) {
		failure := errors.New("a disk that stopped answering")
		matches, err := readerHoldsExactly(iotest.ErrReader(failure), want)
		require.ErrorIs(t, err, failure)
		require.False(t, matches)
	})
}
