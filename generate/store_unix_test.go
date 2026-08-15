//go:build unix

package generate_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// These tests use Unix-only symbolic links and fifos.

// markerFile is the shortest file rasqlgen would have written: the marker on
// its own first line, and a package clause under it.
var markerFile = []byte(genfile.Marker + "\n\npackage store\n")

// TestStorePlanReportsWhereASymlinkedFileResolves pins what File.Resolved is
// for: a planned destination that is a symbolic link pointing out of Dir is
// followed, not refused -- internal/genfile.Write writes through an output
// link on purpose -- so the plan reports the file a commit would actually
// replace instead of leaving File.Path to imply the bytes stay in Dir.
func TestStorePlanReportsWhereASymlinkedFileResolves(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "store")
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.MkdirAll(outside, 0o700))

	escaped := filepath.Join(outside, "escaped_q_gen.go")
	link := filepath.Join(dir, "q_gen.go")
	require.NoError(t, os.Symlink(escaped, link))

	sqlPath := filepath.Join(base, "q.sql")
	require.NoError(t, os.WriteFile(sqlPath, []byte("SELECT 1"), 0o600))

	store := generate.Store{
		Package: "store",
		Dir:     dir,
		Tables:  []schema.TableDef{usersTableDef()},
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{{Input: sqlPath, Function: "Q", Output: "q_gen.go"}},
	}
	plan, err := store.Plan()
	require.NoError(t, err)

	// The temporary directory itself may be reached through a link, so the
	// destinations that resolve to themselves are spelled the way the
	// filesystem reaches them rather than the way the test spelled them.
	realDir, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	realOutside, err := filepath.EvalSymlinks(outside)
	require.NoError(t, err)

	files := make(map[string]generate.File, len(plan.Files()))
	for _, f := range plan.Files() {
		files[filepath.Base(f.Path)] = f
	}
	require.Len(t, files, 4)

	query := files["q_gen.go"]
	require.Equal(t, link, query.Path, "Path is the destination as planned, inside Dir")
	require.Equal(t, filepath.Join(realOutside, "escaped_q_gen.go"), query.Resolved, "Resolved is the file the link points at")
	require.NotEqual(t, query.Path, query.Resolved)

	for name, f := range files {
		if name == "q_gen.go" {
			continue
		}
		require.Equal(t, filepath.Join(realDir, name), f.Resolved, "an ordinary destination resolves to itself")
	}

	// Planning still writes nothing: the link is untouched and its target
	// was not created, and the link is not a leftover to prune either.
	info, err := os.Lstat(link)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&fs.ModeSymlink, "Plan must leave the symbolic link a symbolic link")
	require.NoFileExists(t, escaped, "Plan must not create the file the link points at")
	require.Empty(t, plan.Orphans())
}

// TestStorePlanRejectsTwoFilesThatResolveToOneDestination covers two planned
// files whose distinct names inside Dir are both symbolic links to one file.
// The name check cannot see this -- "one_gen.go" and "two_gen.go" are
// distinct keys -- so the plan used to come back with no error, two files,
// and one destination a commit would write twice and keep only the last of.
// What is refused is the shared destination alone: the single-link case in
// TestStorePlanReportsWhereASymlinkedFileResolves still plans, links included.
func TestStorePlanRejectsTwoFilesThatResolveToOneDestination(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "store")
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.MkdirAll(outside, 0o700))

	target := filepath.Join(outside, "same_gen.go")
	one := filepath.Join(dir, "one_gen.go")
	two := filepath.Join(dir, "two_gen.go")
	require.NoError(t, os.Symlink(target, one))
	require.NoError(t, os.Symlink(target, two))

	sqlPath := filepath.Join(base, "q.sql")
	require.NoError(t, os.WriteFile(sqlPath, []byte("SELECT 1"), 0o600))

	store := generate.Store{
		Package: "store",
		Dir:     dir,
		Tables:  []schema.TableDef{usersTableDef()},
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{
			{Input: sqlPath, Function: "One", Output: "one_gen.go"},
			{Input: sqlPath, Function: "Two", Output: "two_gen.go"},
		},
	}
	plan, err := store.Plan()

	// The temporary directory itself may be reached through a link, so the
	// destination is spelled the way the filesystem reaches it.
	realOutside, evalErr := filepath.EvalSymlinks(outside)
	require.NoError(t, evalErr)
	require.ErrorContains(t, err, "both resolve to "+filepath.Join(realOutside, "same_gen.go"))
	require.ErrorContains(t, err, one)
	require.ErrorContains(t, err, two)
	require.Empty(t, plan.Files(), "a rejected Store must plan no files at all")
	require.NoFileExists(t, target, "Plan must not create the file the links point at")
}

// TestPlanCommitRefusesAPlannedFileThatResolvesOntoALeftover covers a planned
// destination that a symbolic link sends onto a file the same run deletes.
// Nothing is written twice here and nothing is merely stale: step 2 writes
// the planned bytes into the leftover, step 3 deletes the leftover, and both
// files are gone while Commit reports success. Commit resolves every
// destination afresh and compares it against what it is about to delete, so
// the run is refused before the first write.
func TestPlanCommitRefusesAPlannedFileThatResolvesOntoALeftover(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, generate.WritePackage("store", dir, usersTableDef()))

	orphan := filepath.Join(dir, "dropped_gen.go")
	require.NoError(t, os.WriteFile(orphan, markerFile, 0o600))

	store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef()}, Prune: true}
	plan, err := store.Plan()
	require.NoError(t, err)
	require.Equal(t, []string{orphan}, plan.Orphans())

	// The link appears after Store.Plan looked, which is why the plan's own
	// resolved destinations cannot see it.
	planned := filepath.Join(dir, "users_gen.go")
	require.NoError(t, os.Remove(planned))
	require.NoError(t, os.Symlink(orphan, planned))

	err = plan.Commit()
	require.ErrorContains(t, err, "deletes as a leftover")
	require.ErrorContains(t, err, orphan)

	// Both files survive: the leftover still holds its own bytes, and the
	// link still points at it rather than dangling over a deleted file.
	leftover, readErr := os.ReadFile(orphan)
	require.NoError(t, readErr)
	require.Equal(t, markerFile, leftover)
	info, statErr := os.Lstat(planned)
	require.NoError(t, statErr)
	require.NotZero(t, info.Mode()&fs.ModeSymlink)
}

// TestStoreWriteRefusesAPlannedFileAlreadyLinkedToALeftover is the same loss
// through one Store.Write, with the link already in place before the plan is
// built rather than appearing after it. Store.Plan cannot refuse it: the link
// resolves to a file carrying rasqlgen's marker, and that file is a leftover
// precisely because no planned path names it. Commit is where the two facts
// meet.
func TestStoreWriteRefusesAPlannedFileAlreadyLinkedToALeftover(t *testing.T) {
	dir := t.TempDir()
	orphan := filepath.Join(dir, "dropped_gen.go")
	require.NoError(t, os.WriteFile(orphan, markerFile, 0o600))
	require.NoError(t, os.Symlink(orphan, filepath.Join(dir, "users_gen.go")))

	store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef()}, Prune: true}
	require.ErrorContains(t, store.Write(), "deletes as a leftover")

	leftover, err := os.ReadFile(orphan)
	require.NoError(t, err)
	require.Equal(t, markerFile, leftover)
}

// TestPlanCommitRefusesALeftoverThatIsNoLongerARegularFile replaces a
// recorded leftover with a fifo nothing is writing to. Commit re-reads every
// leftover's first line, and opening a fifo blocks until something opens the
// other end, so without a mode check first the generator hangs for good --
// no error, no timeout, and a build or CI run waiting on it. The check
// matches the one genfile.ResolveDestination already makes before it
// overwrites anything.
func TestPlanCommitRefusesALeftoverThatIsNoLongerARegularFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, generate.WritePackage("store", dir, usersTableDef()))

	orphan := filepath.Join(dir, "dropped_gen.go")
	require.NoError(t, os.WriteFile(orphan, markerFile, 0o600))

	store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef()}, Prune: true}
	plan, err := store.Plan()
	require.NoError(t, err)
	require.Equal(t, []string{orphan}, plan.Orphans())

	require.NoError(t, os.Remove(orphan))
	require.NoError(t, syscall.Mkfifo(orphan, 0o644))

	// Commit runs on its own goroutine so a regression is a failed test
	// rather than a test binary that never finishes. Nothing ever opens the
	// fifo for writing, so a Commit that opens it does not come back.
	done := make(chan error, 1)
	go func() { done <- plan.Commit() }()
	select {
	case err = <-done:
		require.ErrorContains(t, err, "is not a regular file")
		require.ErrorContains(t, err, orphan)
	case <-time.After(10 * time.Second):
		t.Fatal("Commit blocked on the fifo instead of refusing it")
	}

	info, statErr := os.Lstat(orphan)
	require.NoError(t, statErr)
	require.NotZero(t, info.Mode()&fs.ModeNamedPipe, "Commit must leave the fifo alone")
}

// TestPlanCommitRefusesADirectoryItNoLongerRecognizes replaces the output
// directory itself with a symbolic link to another directory between Plan and
// Commit. Every path Commit holds is a string, and the kernel resolves every
// component of it afresh on each call, so a leftover recorded in one
// directory was being deleted from another -- a file outside Dir that the
// plan never read, authorized by a marker check that had followed the same
// swapped path. Commit compares the directory it opens against the one
// Store.Plan listed and refuses when they differ.
func TestPlanCommitRefusesADirectoryItNoLongerRecognizes(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "store")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, generate.WritePackage("store", dir, usersTableDef()))

	orphan := filepath.Join(dir, "dropped_gen.go")
	require.NoError(t, os.WriteFile(orphan, markerFile, 0o600))

	store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef()}, Prune: true}
	plan, err := store.Plan()
	require.NoError(t, err)
	require.Equal(t, []string{orphan}, plan.Orphans())

	// elsewhere holds a file with the same name, carrying the same marker,
	// so the per-file marker check passes when it is followed there.
	elsewhere := filepath.Join(base, "elsewhere")
	require.NoError(t, os.MkdirAll(elsewhere, 0o700))
	bystander := filepath.Join(elsewhere, "dropped_gen.go")
	require.NoError(t, os.WriteFile(bystander, markerFile, 0o600))

	require.NoError(t, os.RemoveAll(dir))
	require.NoError(t, os.Symlink(elsewhere, dir))

	err = plan.Commit()
	require.ErrorContains(t, err, "no longer the directory Store.Plan read")

	// Nothing outside the directory the plan read was touched: the bystander
	// still holds its bytes, and no planned file landed beside it.
	survivor, readErr := os.ReadFile(bystander)
	require.NoError(t, readErr)
	require.Equal(t, markerFile, survivor)
	entries, readDirErr := os.ReadDir(elsewhere)
	require.NoError(t, readDirErr)
	require.Len(t, entries, 1, "Commit must write nothing into a directory the plan never read")
}
