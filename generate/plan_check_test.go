// This file is package generate, not generate_test, because
// TestPlanCheckNeverReachesTheWriteSeams swaps writeGeneratedFile and
// removeGeneratedFile for stand-ins that fail the test if they are ever
// called. Both are unexported package-level seams, so only a test inside
// this package can reach one, the same way store_commit_test.go does for
// the write order. The helper table definitions it uses are that file's,
// which is the same package.
package generate

import (
	"os"
	"path/filepath"
	"testing"

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
