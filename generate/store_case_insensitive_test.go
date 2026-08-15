package generate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// caseInsensitiveTempDir returns an empty temporary directory on a
// filesystem that holds one entry for two names differing only in case, and
// skips the test when the temporary directory is not on such a filesystem.
//
// The filesystem is asked rather than the platform: macOS's default APFS and
// Windows' NTFS ignore case in file names, an ordinary Linux ext4 checkout
// does not, and either can be mounted under the other. A skip here is the
// honest result on ext4, where a_gen.go and A_gen.go really are two files
// and there is nothing for these tests to regress. Point TMPDIR at a
// directory on a case-insensitive filesystem to run them anywhere.
func caseInsensitiveTempDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	probe := filepath.Join(dir, "probe_gen.go")
	require.NoError(t, os.WriteFile(probe, []byte("probe\n"), 0o600))
	written, err := os.Lstat(probe)
	require.NoError(t, err)

	variant, err := os.Lstat(filepath.Join(dir, "PROBE_gen.go"))
	if err != nil || !os.SameFile(written, variant) {
		t.Skipf("%s is on a filesystem that tells file names apart by case; set TMPDIR to a case-insensitive one to run this test", dir)
	}

	require.NoError(t, os.Remove(probe))
	return dir
}

// TestStorePlanKeepsACaseVariantOfAPlannedFile is the regression test for a
// lost file. A directory holding Q_gen.go, and a store that plans q_gen.go
// with Prune set, name one entry on a filesystem that ignores case in file
// names. Recorded as separate strings, Q_gen.go was an orphan the run
// deleted, so Commit wrote the per-table file in step 2 and step 3 deleted
// that same entry again: no q_gen.go on disk afterwards, and Commit
// reporting success.
//
// The entry is the planned file, so it is not an orphan, and every planned
// file must hold its own planned bytes when Commit returns. A leftover that
// is genuinely nothing the plan writes is still pruned alongside it, which
// is what keeps this from being a fix that simply stops deleting.
func TestStorePlanKeepsACaseVariantOfAPlannedFile(t *testing.T) {
	dir := caseInsensitiveTempDir(t)
	marker := []byte(genfile.Marker + "\n\npackage store\n")

	variant := filepath.Join(dir, "Q_gen.go")
	require.NoError(t, os.WriteFile(variant, marker, 0o600))
	leftover := filepath.Join(dir, "dropped_gen.go")
	require.NoError(t, os.WriteFile(leftover, marker, 0o600))

	store := generate.Store{
		Package: "store",
		Dir:     dir,
		Tables:  []schema.TableDef{namedTableDef("q")},
		Prune:   true,
	}
	plan, err := store.Plan()
	require.NoError(t, err)
	require.Equal(t, []string{leftover}, plan.Orphans(),
		"Q_gen.go is the entry the plan writes q_gen.go into, so only dropped_gen.go is a leftover")

	require.NoError(t, plan.Commit())

	for _, f := range plan.Files() {
		written, readErr := os.ReadFile(f.Path)
		require.NoError(t, readErr, "%s must survive the commit that wrote it", f.Path)
		require.Equal(t, f.Source, written)
	}
	require.NoFileExists(t, leftover)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	require.Len(t, names, len(plan.Files()),
		"the directory holds exactly the planned files, under one spelling each: %v", names)
}

// TestPlanCommitPrunesALeftoverSpelledApartFromAPlannedFile is the guard on
// the rule that refuses two destinations nothing on disk tells apart: that
// rule must not reach a pair the filesystem has already answered for. The
// leftover Dropped_gen.go folds together with the planned dropped_gen.go and
// exists, and one spelling naming a file while the other names nothing is the
// filesystem saying they are two files.
//
// It runs on every filesystem rather than only a case-insensitive one,
// because the outcome asserted here holds either way and neither form of it
// is a refusal: where the two names are one entry it is the planned file and
// there is nothing to prune, and where they are two files the leftover is
// pruned and the planned file written beside where it was.
func TestPlanCommitPrunesALeftoverSpelledApartFromAPlannedFile(t *testing.T) {
	dir := t.TempDir()
	leftover := filepath.Join(dir, "Dropped_gen.go")
	require.NoError(t, os.WriteFile(leftover, []byte(genfile.Marker+"\n\npackage store\n"), 0o600))

	sqlPath := filepath.Join(t.TempDir(), "q.sql")
	require.NoError(t, os.WriteFile(sqlPath, []byte("SELECT 1"), 0o600))

	store := generate.Store{
		Package: "store",
		Dir:     dir,
		Tables:  []schema.TableDef{usersTableDef()},
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{{Input: sqlPath, Function: "Dropped", Output: "dropped_gen.go"}},
		Prune:   true,
	}
	plan, err := store.Plan()
	require.NoError(t, err)
	require.NoError(t, plan.Commit())

	for _, f := range plan.Files() {
		written, readErr := os.ReadFile(f.Path)
		require.NoError(t, readErr, "%s must hold its own planned bytes", f.Path)
		require.Equal(t, f.Source, written)
	}
}

// TestStorePlanRejectsTwoQueryOutputsThatShareAnEntry is the other half of
// the same defect, on the filesystem that makes it real: two query outputs
// spelled apart only by case are one file here, and a plan that wrote both
// would keep only the last. Store.Plan refuses the pair on every platform,
// which TestStorePlanRejectsCollisions pins as well; this one confirms the
// refusal is what stands between the store and a directory that really does
// hold a single entry for the two names.
func TestStorePlanRejectsTwoQueryOutputsThatShareAnEntry(t *testing.T) {
	dir := caseInsensitiveTempDir(t)

	sqlPath := filepath.Join(t.TempDir(), "q.sql")
	require.NoError(t, os.WriteFile(sqlPath, []byte("SELECT 1"), 0o600))

	store := generate.Store{
		Package: "store",
		Dir:     dir,
		Tables:  []schema.TableDef{usersTableDef()},
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{
			{Input: sqlPath, Function: "QueryOne", Output: "a_gen.go"},
			{Input: sqlPath, Function: "QueryTwo", Output: "A_gen.go"},
		},
	}
	require.ErrorContains(t, store.Write(), "collides")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries, "a refused store must write nothing at all")
}
