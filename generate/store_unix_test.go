//go:build unix

package generate_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

func TestStoreHandlesMarkedGeneratedSymlinkOrphanCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name         string
		prune        bool
		write        func(generate.Store, generate.Plan) error
		wantWriteErr bool
	}{
		{
			name:         "without Prune Store.Check refuses and Plan.Commit leaves the symlink",
			write:        func(_ generate.Store, plan generate.Plan) error { return plan.Commit() },
			wantWriteErr: true,
		},
		{
			name:  "with Prune Store.Check reports stale and Plan.Commit removes only the symlink",
			prune: true,
			write: func(_ generate.Store, plan generate.Plan) error { return plan.Commit() },
		},
		{
			name:  "with Prune Store.Check reports stale and Store.Write removes only the symlink",
			prune: true,
			write: func(store generate.Store, _ generate.Plan) error { return store.Write() },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, "store")
			target := filepath.Join(base, "old_gen.go")
			require.NoError(t, os.MkdirAll(dir, 0o700))
			require.NoError(t, os.WriteFile(target, markerFile, 0o600))
			orphan := filepath.Join(dir, "old_gen.go")
			require.NoError(t, os.Symlink(target, orphan))

			store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef()}, Prune: tc.prune}
			plan, err := store.Plan()
			require.NoError(t, err)
			require.Equal(t, []string{orphan}, plan.Orphans())

			checkErr := store.Check()
			require.Error(t, checkErr)
			require.ErrorContains(t, checkErr, orphan)
			if tc.prune {
				require.True(t, errors.Is(checkErr, generate.ErrStale))
				require.ErrorContains(t, checkErr, "would be deleted")
			} else {
				require.False(t, errors.Is(checkErr, generate.ErrStale), "a symlink orphan without Prune is a refusal, not staleness")
				require.ErrorContains(t, checkErr, "Store.Prune is false")
			}
			requireSymlinkTo(t, orphan, target)
			requireFileBytes(t, target, markerFile)

			err = tc.write(store, plan)
			if tc.wantWriteErr {
				require.Error(t, err)
				require.ErrorContains(t, err, orphan)
				requireSymlinkTo(t, orphan, target)
				for _, f := range plan.Files() {
					require.NoFileExists(t, f.Path)
				}
			} else {
				require.NoError(t, err)
				_, err := os.Lstat(orphan)
				require.ErrorIs(t, err, fs.ErrNotExist)
				for _, f := range plan.Files() {
					require.FileExists(t, f.Path)
				}
			}
			requireFileBytes(t, target, markerFile)
		})
	}
}

func requireSymlinkTo(t *testing.T, link string, target string) {
	t.Helper()

	info, err := os.Lstat(link)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&fs.ModeSymlink, "the orphan path must stay a symbolic link")
	got, err := os.Readlink(link)
	require.NoError(t, err)
	require.Equal(t, target, got)
}

func requireFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

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

// TestStoreWriteWritesThroughAPlannedSymlink is the control for the
// directory handles Commit writes through: a planned destination that is a
// symbolic link out of Dir is still written through, not refused, so the
// bytes land in the file the link names and the link itself survives.
func TestStoreWriteWritesThroughAPlannedSymlink(t *testing.T) {
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
	require.NoError(t, store.Write())

	info, err := os.Lstat(link)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&fs.ModeSymlink, "the link itself survives the write")
	source, err := os.ReadFile(escaped)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(source), genfile.Marker), "the query's own bytes land in the file the link names")
}

// TestPlanCommitRefusesADirectoryFirstCreatedAsASymlink covers a Dir that
// does not exist when Store.Plan runs and is a symbolic link to an
// unrelated writable directory by the time Commit does. A plan for a
// missing Dir has no directory of its own to compare against, so Commit
// used to create the path -- which now stats as an existing directory
// through the link -- open it, and write every planned file wherever the
// link pointed. The plan records the closest existing directory above Dir
// instead, and Commit walks down from there and refuses a component that is
// a link rather than a directory of its own.
// How the link names the directory it points at decides which refusal
// catches it. A relative target that stays inside the directory above Dir is
// one an open directory handle follows on its own, so it is the case the
// walk's own check on each component is there for; an absolute target is
// refused a layer below, because a handle refuses an absolute link outright.
func TestPlanCommitRefusesADirectoryFirstCreatedAsASymlink(t *testing.T) {
	for _, tc := range []struct {
		name     string
		relative bool
		message  string
	}{
		{name: "a relative link inside the directory above Dir", relative: true, message: "symbolic link"},
		{name: "an absolute link"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, "store")
			elsewhere := filepath.Join(base, "elsewhere")
			require.NoError(t, os.MkdirAll(elsewhere, 0o700))
			require.NoDirExists(t, dir)

			store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef()}, Prune: true}
			plan, err := store.Plan()
			require.NoError(t, err)
			require.Empty(t, plan.Orphans())

			// The link appears strictly between Plan and Commit, which is
			// the held plan Commit promises to survive rather than a race
			// inside one running commit.
			target := elsewhere
			if tc.relative {
				target = "elsewhere"
			}
			require.NoError(t, os.Symlink(target, dir))

			err = plan.Commit()
			require.ErrorContains(t, err, "refusing to commit into "+dir)
			if tc.message != "" {
				require.ErrorContains(t, err, tc.message)
			}

			entries, readErr := os.ReadDir(elsewhere)
			require.NoError(t, readErr)
			require.Empty(t, entries, "Commit must write nothing into a directory the plan never read")
			info, statErr := os.Lstat(dir)
			require.NoError(t, statErr)
			require.NotZero(t, info.Mode()&fs.ModeSymlink, "Commit must leave the link alone")
		})
	}
}

// TestPlanCommitKeepsDirectoriesItCreatedWhenTheWalkFails pins both halves
// of what Plan.Commit's doc comment promises for a failure before the first
// write: no generated file is written, and the directory components step 1
// already created on its way down to Dir stay on disk.
//
// Dir here is three components below an existing directory and its last
// component is longer than the filesystem accepts, so the walk creates the
// two components above it and then fails to create the third. Removing
// those two again would be rollback this package does not do: a Mkdir that
// returned nil does not prove this call created the entry rather than
// winning a race, and the directory could already be in use by whoever else
// created it.
func TestPlanCommitKeepsDirectoriesItCreatedWhenTheWalkFails(t *testing.T) {
	tooLong := strings.Repeat("n", 300)
	if err := os.Mkdir(filepath.Join(t.TempDir(), tooLong), 0o700); err == nil {
		t.Skip("this filesystem accepts a 300-character name, so the walk cannot be made to fail on one")
	}

	base := t.TempDir()
	dir := filepath.Join(base, "x", "y", tooLong)
	store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef()}}
	plan, err := store.Plan()
	require.NoError(t, err)

	entries, err := os.ReadDir(base)
	require.NoError(t, err)
	require.Empty(t, entries, "planning a Dir that does not exist must create nothing")

	err = plan.Commit()
	require.ErrorContains(t, err, "refusing to commit into "+dir)

	require.DirExists(t, filepath.Join(base, "x"), "a component the walk created before the refusal stays")
	require.DirExists(t, filepath.Join(base, "x", "y"), "a component the walk created before the refusal stays")
	require.NoDirExists(t, dir)

	var files []string
	require.NoError(t, filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			files = append(files, path)
		}
		return nil
	}))
	require.Empty(t, files, "a refusal in step 1 must write no file at all")
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

// TestStorePlanRejectsTwoLinksToMissingNamesThatFoldTogether covers the pair
// TestStorePlanRejectsTwoFilesThatResolveToOneDestination cannot reach: two
// planned files inside Dir are symbolic links whose targets differ only in
// case, and neither target exists yet. Asking the filesystem which file each
// destination is has no answer before either of them exists, and reading that
// silence as "two files" let the plan through -- a commit then wrote both
// spellings into the single entry a filesystem that ignores case in file
// names holds for the two, deleted nothing, and reported success with one
// planned file's bytes nowhere on disk.
//
// The pair is refused on every filesystem, which is also why this test needs
// no case-insensitive mount: Store.Plan already folds the names inside Dir
// everywhere, for the same reason.
func TestStorePlanRejectsTwoLinksToMissingNamesThatFoldTogether(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "store")
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.MkdirAll(outside, 0o700))

	upper := filepath.Join(outside, "A_gen.go")
	lower := filepath.Join(outside, "a_gen.go")
	require.NoError(t, os.Symlink(upper, filepath.Join(dir, "aa_gen.go")))
	require.NoError(t, os.Symlink(lower, filepath.Join(dir, "bb_gen.go")))

	store := generate.Store{
		Package: "store",
		Dir:     dir,
		Tables:  []schema.TableDef{namedTableDef("aa"), namedTableDef("bb")},
	}
	plan, err := store.Plan()
	require.ErrorContains(t, err, "both resolve to")
	require.ErrorContains(t, err, "nothing on disk tells apart")
	require.Empty(t, plan.Files(), "a rejected Store must plan no files at all")

	require.NoFileExists(t, upper, "Plan must not create the file either link points at")
	require.NoFileExists(t, lower, "Plan must not create the file either link points at")
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// TestPlanCommitRefusesTwoLinksToMissingNamesThatFoldTogether is that same
// pair appearing after the plan was built, which is the window Commit
// re-resolves every destination for. Store.Plan cannot see it: when it ran,
// both planned paths were ordinary names inside Dir that resolved to
// themselves.
func TestPlanCommitRefusesTwoLinksToMissingNamesThatFoldTogether(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "store")
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.MkdirAll(outside, 0o700))

	store := generate.Store{
		Package: "store",
		Dir:     dir,
		Tables:  []schema.TableDef{namedTableDef("aa"), namedTableDef("bb")},
	}
	plan, err := store.Plan()
	require.NoError(t, err)

	upper := filepath.Join(outside, "A_gen.go")
	lower := filepath.Join(outside, "a_gen.go")
	require.NoError(t, os.Symlink(upper, filepath.Join(dir, "aa_gen.go")))
	require.NoError(t, os.Symlink(lower, filepath.Join(dir, "bb_gen.go")))

	err = plan.Commit()
	require.ErrorContains(t, err, "refusing to commit")
	require.ErrorContains(t, err, "nothing on disk tells apart")

	require.NoFileExists(t, upper)
	require.NoFileExists(t, lower)
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	require.Empty(t, entries, "a refused commit must write nothing at all")
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

// TestPlanCommitRefusesAPlannedFileThatResolvesOntoALeftoverSpelledApart is
// the same loss reached through a spelling rather than a path. The recorded
// leftover is Dropped_gen.go and the link sends the planned file to
// dropped_gen.go, which is that same entry on a filesystem that ignores case
// in file names. Commit compares the two by asking the filesystem, so the
// run is refused here too; comparing the resolved paths as strings let it
// through, and step 3 then deleted what step 2 had just written.
func TestPlanCommitRefusesAPlannedFileThatResolvesOntoALeftoverSpelledApart(t *testing.T) {
	dir := caseInsensitiveTempDir(t)
	require.NoError(t, generate.WritePackage("store", dir, usersTableDef()))

	orphan := filepath.Join(dir, "Dropped_gen.go")
	require.NoError(t, os.WriteFile(orphan, markerFile, 0o600))

	store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef()}, Prune: true}
	plan, err := store.Plan()
	require.NoError(t, err)
	require.Equal(t, []string{orphan}, plan.Orphans())

	// The link appears after Store.Plan looked, which is why the plan's own
	// resolved destinations cannot see it, and it names the leftover under
	// the spelling the plan did not record.
	planned := filepath.Join(dir, "users_gen.go")
	require.NoError(t, os.Remove(planned))
	require.NoError(t, os.Symlink(filepath.Join(dir, "dropped_gen.go"), planned))

	err = plan.Commit()
	require.ErrorContains(t, err, "deletes as a leftover")
	require.ErrorContains(t, err, orphan)

	leftover, readErr := os.ReadFile(orphan)
	require.NoError(t, readErr)
	require.Equal(t, markerFile, leftover)
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
