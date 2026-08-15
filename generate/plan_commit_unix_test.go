//go:build unix

// This file is package generate, not generate_test, because every test in
// it changes the output directory at an exact point inside one running
// Commit: writeGeneratedFile is an unexported package-level seam, so only a
// test inside this package can swap it, the same way store_commit_test.go
// does for the write order. Swapping the seam is what makes these
// deterministic -- the change lands between two of Commit's own steps
// rather than being raced against them -- and unix is for the symbolic
// links.
package generate

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// commitTestMarkerFile is the shortest file rasqlgen would have written:
// the marker on its own first line, and a package clause under it.
var commitTestMarkerFile = []byte(genfile.Marker + "\n\npackage store\n")

// mutateOnWriteOf swaps writeGeneratedFile for a stand-in that runs mutate
// immediately before the file called name is written, and puts the seam
// back when the test ends. Every write still happens: the stand-in hands
// the call straight on.
func mutateOnWriteOf(t *testing.T, name string, mutate func()) {
	t.Helper()
	original := writeGeneratedFile
	t.Cleanup(func() { writeGeneratedFile = original })
	writeGeneratedFile = func(dir *os.Root, written string, source []byte) error {
		if written == name {
			mutate()
		}
		return original(dir, written, source)
	}
}

// TestPlanCommitRefusesADestinationReplacedAfterItWasAuthorized covers a
// planned destination that step 1 resolved to a file inside Dir and that a
// symbolic link takes over before step 4 writes it. Handing the planned
// path to the writer had it resolve that path a second time, so the write
// followed the new link and the aggregator landed outside Dir while Commit
// reported success. Commit now writes through the directory handle it
// authorized, by the destination's own name, so the link is refused instead
// of followed and nothing lands outside.
func TestPlanCommitRefusesADestinationReplacedAfterItWasAuthorized(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "store")
	elsewhere := filepath.Join(base, "elsewhere")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.MkdirAll(elsewhere, 0o700))

	store := Store{Package: "store", Dir: dir, Tables: []schema.TableDef{commitTestUsersDef()}}
	plan, err := store.Plan()
	require.NoError(t, err)

	planned := filepath.Join(dir, schemaDescriptorFilename)
	escaped := filepath.Join(elsewhere, schemaDescriptorFilename)
	mutateOnWriteOf(t, schemaDescriptorFilename, func() {
		require.NoError(t, os.Symlink(escaped, planned))
	})

	err = plan.Commit()
	require.ErrorContains(t, err, "is not a regular file")
	require.NoFileExists(t, escaped, "Commit must write nothing into a directory it never authorized")

	entries, readErr := os.ReadDir(elsewhere)
	require.NoError(t, readErr)
	require.Empty(t, entries)
	info, statErr := os.Lstat(planned)
	require.NoError(t, statErr)
	require.NotZero(t, info.Mode()&fs.ModeSymlink, "Commit must leave the link it refused alone")
}

// TestPlanCommitRefusesADestinationRedirectedOntoAFileItWrote is the same
// replacement pointed at a file this very run has already written, which is
// the two-planned-files-one-destination case step 1 refuses at preflight
// reached after that preflight has run. Following the new link made step 4
// write the aggregator over the per-table file written in step 2, leaving
// one file where two belong and Commit still reporting success.
func TestPlanCommitRefusesADestinationRedirectedOntoAFileItWrote(t *testing.T) {
	dir := t.TempDir()

	store := Store{Package: "store", Dir: dir, Tables: []schema.TableDef{commitTestUsersDef()}}
	plan, err := store.Plan()
	require.NoError(t, err)

	planned := filepath.Join(dir, schemaDescriptorFilename)
	table := filepath.Join(dir, "users_gen.go")
	mutateOnWriteOf(t, schemaDescriptorFilename, func() {
		require.NoError(t, os.Symlink(table, planned))
	})

	err = plan.Commit()
	require.ErrorContains(t, err, "is not a regular file")

	source, readErr := os.ReadFile(table)
	require.NoError(t, readErr)
	require.True(t, bytes.HasPrefix(source, []byte(genfile.Marker)))
	require.NotContains(t, string(source), "func Tables(", "the per-table file must not be overwritten by the aggregator")
}

// TestPlanCommitRefusesToDeleteALeftoverReplacedMidCommit covers a recorded
// leftover that a different file takes the name of after step 1 checked it
// and before step 3 deletes it. The open directory handle both go through
// pins the directory, not the entry: each call resolves the bare name
// afresh there, so the delete used to land on a file no marker check had
// ever looked at. The replacement here carries the marker itself, so
// re-reading the first line alone still passes; only the identity tells the
// two apart.
func TestPlanCommitRefusesToDeleteALeftoverReplacedMidCommit(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, WritePackage("store", dir, commitTestUsersDef()))
	orphan := filepath.Join(dir, "dropped_gen.go")
	require.NoError(t, os.WriteFile(orphan, commitTestMarkerFile, 0o600))

	store := Store{Package: "store", Dir: dir, Tables: []schema.TableDef{commitTestUsersDef()}, Prune: true}
	plan, err := store.Plan()
	require.NoError(t, err)
	require.Equal(t, []string{orphan}, plan.Orphans())

	replacement := []byte(genfile.Marker + "\n\npackage store\n\n// a different file entirely\n")
	mutateOnWriteOf(t, "users_gen.go", func() {
		staged := filepath.Join(dir, "staged")
		require.NoError(t, os.WriteFile(staged, replacement, 0o600))
		require.NoError(t, os.Rename(staged, orphan))
	})

	err = plan.Commit()
	require.ErrorContains(t, err, "no longer the file this commit checked")
	require.ErrorContains(t, err, orphan)

	survivor, readErr := os.ReadFile(orphan)
	require.NoError(t, readErr)
	require.Equal(t, replacement, survivor, "the file that took the leftover's name must survive")
}
