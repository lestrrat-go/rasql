package generate_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// treeEntry is what snapshotTree records for one entry in a directory: its
// mode, and its bytes when it is a regular file. Recording the mode
// alongside the content is what makes an entry that changed kind, or a file
// rewritten with the same length, a difference rather than a match.
type treeEntry struct {
	Mode    fs.FileMode
	Content string
}

// snapshotTree records the whole of dir -- every entry at every depth, not
// only the generated files a caller expects -- so a before/after comparison
// fails on any write, deletion, creation, or mode change anywhere in it. A
// dir that does not exist records nothing, which is how a call that created
// it shows up as a difference.
//
// It is snapshotDir's stricter counterpart: that one reads the regular
// files directly in a directory, which cannot see a subdirectory appearing,
// an entry losing its mode bits, or a file being replaced by something that
// is not a file.
func snapshotTree(t *testing.T, dir string) map[string]treeEntry {
	t.Helper()
	tree := make(map[string]treeEntry)
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return tree
	}
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		recorded := treeEntry{Mode: info.Mode()}
		if info.Mode().IsRegular() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			recorded.Content = string(content)
		}
		tree[relative] = recorded
		return nil
	}))
	return tree
}

// TestStoreCheckPassesRightAfterWrite confirms Check is clean immediately
// after the Write that produced the directory it checks.
func TestStoreCheckPassesRightAfterWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef(), ordersTableDef()}}
	require.NoError(t, store.Write())
	require.NoError(t, store.Check())
}

// TestStoreCheckReportsStaleWhenAFileIsMissing deletes one generated file
// and confirms Check reports it as missing, wrapping ErrStale.
func TestStoreCheckReportsStaleWhenAFileIsMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef(), ordersTableDef()}}
	require.NoError(t, store.Write())

	ordersPath := filepath.Join(dir, "orders_gen.go")
	require.NoError(t, os.Remove(ordersPath))

	err := store.Check()
	require.Error(t, err)
	require.True(t, errors.Is(err, generate.ErrStale))
	require.ErrorContains(t, err, ordersPath)
	require.ErrorContains(t, err, "is missing")
}

// TestStoreCheckReportsStaleWhenAFileDiffers rewrites one generated file's
// body -- keeping its marker line intact, so it is still a file rasqlgen
// wrote -- and confirms Check reports it as differing, wrapping ErrStale.
func TestStoreCheckReportsStaleWhenAFileDiffers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef(), ordersTableDef()}}
	require.NoError(t, store.Write())

	ordersPath := filepath.Join(dir, "orders_gen.go")
	original, err := os.ReadFile(ordersPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(ordersPath, append(append([]byte(nil), original...), []byte("\n// a hand edit that keeps the marker\n")...), 0o600))

	err = store.Check()
	require.Error(t, err)
	require.True(t, errors.Is(err, generate.ErrStale))
	require.ErrorContains(t, err, ordersPath)
	require.ErrorContains(t, err, "differs")
}

// TestStoreCheckReportsEveryStaleFileSortedByPath removes one file and
// rewrites another, and confirms Check names both, in path order.
func TestStoreCheckReportsEveryStaleFileSortedByPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef(), ordersTableDef()}}
	require.NoError(t, store.Write())

	ordersPath := filepath.Join(dir, "orders_gen.go")
	usersPath := filepath.Join(dir, "users_gen.go")
	require.NoError(t, os.Remove(ordersPath))
	original, err := os.ReadFile(usersPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(usersPath, append(append([]byte(nil), original...), []byte("\n// a hand edit\n")...), 0o600))

	err = store.Check()
	require.Error(t, err)
	require.True(t, errors.Is(err, generate.ErrStale))
	message := err.Error()
	require.Contains(t, message, ordersPath)
	require.Contains(t, message, usersPath)
	// "orders_gen.go" sorts before "users_gen.go" in the same directory.
	require.Less(t, strings.Index(message, ordersPath), strings.Index(message, usersPath))
}

// TestStoreCheckOnAFreshDirectoryIsStale confirms a Store that has never
// been written is reported as stale -- every file missing -- rather than
// erroring some other way, and that none of the directories a Write would
// create on the way to Dir is created here. Dir is several components below
// an existing directory because that is where Commit's own step 1 creates
// the most: it walks down from the deepest directory that exists, creating
// every component below it, and Check walks the same components creating
// none of them.
func TestStoreCheckOnAFreshDirectoryIsStale(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "missing", "parent", "store")
	require.NoDirExists(t, dir)
	before := snapshotTree(t, base)
	store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef()}}

	err := store.Check()
	require.Error(t, err)
	require.True(t, errors.Is(err, generate.ErrStale))
	require.NoDirExists(t, dir, "Check must not create the directory it found missing")
	require.Equal(t, before, snapshotTree(t, base), "Check must not create any component on the way to a missing Dir")
}

// TestStoreCheckRefusesLeftoversWithoutPrune confirms a leftover file
// rasqlgen wrote makes Check fail the same way Commit would, and that
// errors.Is(err, ErrStale) is false: a refusal is not staleness, because no
// amount of regenerating fixes it -- only Prune, or removing the file by
// hand, does.
func TestStoreCheckRefusesLeftoversWithoutPrune(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, generate.WritePackage("store", dir, usersTableDef()))
	orphan := filepath.Join(dir, "dropped_gen.go")
	require.NoError(t, os.WriteFile(orphan, []byte(genfile.Marker+"\n\npackage store\n"), 0o600))

	store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef()}}
	err := store.Check()
	require.Error(t, err)
	require.False(t, errors.Is(err, generate.ErrStale), "a leftover without Prune is a refusal, not staleness")
	require.ErrorContains(t, err, orphan)
}

// TestStoreCheckReportsStaleLeftoversWithPrune confirms that, with Prune
// set, the same leftover is staleness instead of a refusal: Write would
// delete it, so Check reports it wrapped in ErrStale, naming it "would be
// deleted".
func TestStoreCheckReportsStaleLeftoversWithPrune(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, generate.WritePackage("store", dir, usersTableDef()))
	orphan := filepath.Join(dir, "dropped_gen.go")
	require.NoError(t, os.WriteFile(orphan, []byte(genfile.Marker+"\n\npackage store\n"), 0o600))

	store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef()}, Prune: true}
	err := store.Check()
	require.Error(t, err)
	require.True(t, errors.Is(err, generate.ErrStale))
	require.ErrorContains(t, err, orphan)
	require.ErrorContains(t, err, "would be deleted")
}

// TestStoreCheckDoesNotWrite confirms Check never touches the filesystem:
// every entry's bytes and mode, and the whole directory listing at every
// depth, are unchanged after a stale Check, after a clean one, and after
// one that reports a leftover it would be Write's job to delete.
func TestStoreCheckDoesNotWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef(), ordersTableDef()}}
	require.NoError(t, store.Write())

	ordersPath := filepath.Join(dir, "orders_gen.go")
	require.NoError(t, os.Remove(ordersPath))
	beforeStale := snapshotTree(t, dir)

	err := store.Check()
	require.Error(t, err)
	require.True(t, errors.Is(err, generate.ErrStale))
	require.Equal(t, beforeStale, snapshotTree(t, dir), "a stale Check must not write, delete, or recreate anything")

	// Regenerate so the directory is clean, then check again.
	require.NoError(t, store.Write())
	beforeClean := snapshotTree(t, dir)
	require.NoError(t, store.Check())
	require.Equal(t, beforeClean, snapshotTree(t, dir), "a clean Check must not write, delete, or recreate anything")

	// The third path through Check is the one with a deletion in front of
	// it: a leftover Write would remove, reported as staleness.
	orphan := filepath.Join(dir, "dropped_gen.go")
	require.NoError(t, os.WriteFile(orphan, []byte(genfile.Marker+"\n\npackage store\n"), 0o600))
	pruning := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef(), ordersTableDef()}, Prune: true}
	beforePrune := snapshotTree(t, dir)
	err = pruning.Check()
	require.Error(t, err)
	require.True(t, errors.Is(err, generate.ErrStale))
	require.Equal(t, beforePrune, snapshotTree(t, dir), "a Check that reports a leftover must not delete it")
}

// TestPlanCheckRefusesADestinationThatStoppedBeingOwned confirms Check
// re-resolves every destination at check time rather than trusting what
// Store.Plan saw: the plan is built while the directory is entirely
// rasqlgen's, and a hand-written file takes one destination afterwards. The
// answer is the resolve error, not ErrStale, because regenerating cannot
// fix a hand-written file being in the way.
func TestPlanCheckRefusesADestinationThatStoppedBeingOwned(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef(), ordersTableDef()}}
	require.NoError(t, store.Write())

	plan, err := store.Plan()
	require.NoError(t, err)

	ordersPath := filepath.Join(dir, "orders_gen.go")
	handWritten := []byte("package store\n\n// hand-written, no marker\n")
	require.NoError(t, os.WriteFile(ordersPath, handWritten, 0o600))

	err = plan.Check()
	require.Error(t, err)
	require.False(t, errors.Is(err, generate.ErrStale), "a destination rasqlgen may not replace is a refusal, not staleness")
	require.ErrorContains(t, err, "not generated by rasqlgen")

	got, readErr := os.ReadFile(ordersPath)
	require.NoError(t, readErr)
	require.Equal(t, handWritten, got, "Check must never touch the file it refused")
}

// TestPlanCheckRefusesAnOrphanThatLostItsMarker confirms Check re-reads
// every recorded leftover's marker at check time, for the same reason it
// re-resolves destinations: the plan is built while the leftover is
// rasqlgen's own, and a hand-written file takes its name afterwards. Commit
// refuses that rather than deleting it, so Check reports the same refusal
// and not ErrStale, even with Prune set.
func TestPlanCheckRefusesAnOrphanThatLostItsMarker(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, generate.WritePackage("store", dir, usersTableDef()))
	orphan := filepath.Join(dir, "dropped_gen.go")
	require.NoError(t, os.WriteFile(orphan, []byte(genfile.Marker+"\n\npackage store\n"), 0o600))

	store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef()}, Prune: true}
	plan, err := store.Plan()
	require.NoError(t, err)
	require.Equal(t, []string{orphan}, plan.Orphans())

	require.NoError(t, os.WriteFile(orphan, []byte("package store\n\n// hand-written, no marker\n"), 0o600))

	err = plan.Check()
	require.Error(t, err)
	require.False(t, errors.Is(err, generate.ErrStale), "a leftover that stopped being rasqlgen's is a refusal, not staleness")
	require.ErrorContains(t, err, "no longer opens with rasqlgen's marker")
}

// TestStoreCheckReportsAnUnownedDestinationAsItself confirms a hand-written
// file with no rasqlgen marker sitting where a generated file belongs
// produces the resolve error, not ErrStale: staleness is a question Check
// only asks about a destination rasqlgen owns, and regenerating can never
// fix a hand-written file being in the way.
func TestStoreCheckReportsAnUnownedDestinationAsItself(t *testing.T) {
	dir := t.TempDir()
	handWritten := []byte("package store\n\n// hand-written, no marker\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "users_gen.go"), handWritten, 0o600))

	store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef()}}
	err := store.Check()
	require.Error(t, err)
	require.False(t, errors.Is(err, generate.ErrStale), "an unowned destination is a refusal, not staleness")
	require.ErrorContains(t, err, "users_gen.go")
	require.ErrorContains(t, err, "not generated by rasqlgen")

	got, readErr := os.ReadFile(filepath.Join(dir, "users_gen.go"))
	require.NoError(t, readErr)
	require.Equal(t, handWritten, got, "Check must never touch the unowned file")
}

// TestStoreCheckNamesStalePathsRelativeToRoot pins how Check's error names a
// stale file inside the store's Root: relative to that Root, whether Root
// was given as a relative path or an absolute one. A relative Root is the
// case that has to be resolved before the plan carries it, since every
// planned path is absolute and nothing relates a relative root to one; left
// unresolved, every file inside Root is named by its absolute path, which is
// the form the message keeps for a file outside Root, and the two cannot be
// told apart.
func TestStoreCheckNamesStalePathsRelativeToRoot(t *testing.T) {
	relativeName := filepath.Join("generated", "store", "users_gen.go")
	for _, tc := range []struct {
		name string
		root func(wd string) string
	}{
		{name: "relative root", root: func(string) string { return "." }},
		{name: "absolute root", root: func(wd string) string { return wd }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			// The working directory is read back rather than taken from
			// t.TempDir(): a temporary directory reached through a symbolic
			// link is spelled one way there and another way here, and this is
			// the spelling every absolute path in the plan is built from.
			wd, err := os.Getwd()
			require.NoError(t, err)

			// Dir is relative as well, and never written, so every planned
			// file is missing and each one is named through Root.
			store := generate.Store{Package: "store", Root: tc.root(wd), Dir: filepath.Join("generated", "store"), Tables: []schema.TableDef{usersTableDef()}}
			err = store.Check()
			require.Error(t, err)
			require.True(t, errors.Is(err, generate.ErrStale))
			require.ErrorContains(t, err, relativeName+": is missing")
			require.NotContains(t, err.Error(), wd, "a file inside Root must be named relative to it, not absolutely")
		})
	}
}

// TestStoreCheckNamesAFileOutsideRootAbsolutely pins the other half of that
// promise: a planned file that is not inside Root has no relative form that
// resolves back to it from Root, so Check names it absolutely. This is what
// makes an absolute name in the message mean "outside Root" rather than
// "Root was spelled relatively".
func TestStoreCheckNamesAFileOutsideRootAbsolutely(t *testing.T) {
	root := t.TempDir()
	// A sibling of root rather than a child of it: nothing under root reaches
	// it, which is what makes it a destination outside Root.
	dir := t.TempDir()
	store := generate.Store{Package: "store", Root: root, Dir: dir, Tables: []schema.TableDef{usersTableDef()}}

	err := store.Check()
	require.Error(t, err)
	require.True(t, errors.Is(err, generate.ErrStale))
	require.ErrorContains(t, err, filepath.Join(dir, "users_gen.go")+": is missing")
}

// TestStoreCheckIsCleanForHintedTables confirms Check agrees with Write when
// Hints are involved, in the two directions the hint's overlay semantics
// allow: the same Hints reproduce the same bytes, and feeding the store's
// own hinted descriptor back in with the hint removed is still clean --
// the consequence documented on Store.Hints, pinned here so a later reader
// does not mistake it for a bug.
func TestStoreCheckIsCleanForHintedTables(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	hints := map[string]generate.TableHint{"users": {RowName: "User"}}
	store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef()}, Hints: hints}
	require.NoError(t, store.Write())
	require.NoError(t, store.Check())

	// The generated package's own descriptor is what generate.TableHint.Apply
	// produces: the overlay this store already applied at Write time. That
	// is exactly what the generated package's own Tables() would hand back,
	// without needing to compile and run the generated code to get it.
	hinted := hints["users"].Apply(usersTableDef())
	storeFromGenerated := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{hinted}}
	require.NoError(t, storeFromGenerated.Check(), "removing the hint must not un-name the row type when the descriptor came from the store's own Tables()")
}
