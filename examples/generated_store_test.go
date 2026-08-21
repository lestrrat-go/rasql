package examples_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// queryTemplate, queryFunction, queryPackage and generatedQuery describe the
// one compiled query examples/store holds.
const (
	queryTemplate  = "user_by_email.sql"
	queryFunction  = "UserByEmail"
	queryPackage   = "store"
	generatedQuery = "user_by_email_gen.go"
)

const staleDriftLine = "\n// invented drift line, written only by a test that expects this file to be reported stale\n"

// TestGeneratedStoreIsCurrent regenerates the checked-in examples/store
// package -- the per-table file, the two descriptor files, and the one
// compiled query -- through generate.Store, the same path `rasql codegen
// generate` takes, and fails when the directory differs from what that
// store plans. The definition and the query are stated here in Go rather
// than read from a snapshot file, so the check needs neither a database
// nor a checked-in copy of the schema.
func TestGeneratedStoreIsCurrent(t *testing.T) {
	requireGeneratedDirectoryIsCurrent(t, exampleStore(t), filepath.Join(repositoryRoot, "examples", "store"))
}

// exampleStore is the generate.Store that plans the checked-in
// examples/store package.
func exampleStore(t *testing.T) generate.Store {
	t.Helper()

	users, err := schema.NewTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
	)
	require.NoError(t, err)

	return generate.Store{
		Package: queryPackage,
		Root:    repositoryRootAbs(t),
		Dir:     filepath.Join("examples", "store"),
		Tables:  []schema.TableDef{users},
		Queries: []generate.Query{{
			Input:    filepath.Join("examples", "store", queryTemplate),
			Function: queryFunction,
			Output:   generatedQuery,
			Dialect:  dialect.PostgreSQL(),
		}},
	}
}

// repositoryRootAbs is repositoryRoot (".." as seen from this package's own
// directory) made absolute, which is what generate.Store.Root needs.
// Store resolves a relative Dir, and a relative Query.Input, by joining
// them directly onto Root; joining repositoryRoot onto Store.Dir the way a
// plain file read in this package does (filepath.Join(repositoryRoot,
// "examples", "store")) would ask Store to resolve "../examples/store"
// against the module root it finds by walking up from the process's own
// working directory, landing one directory above the module root instead
// of inside it.
func repositoryRootAbs(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(repositoryRoot)
	require.NoError(t, err)
	return abs
}

// requireGeneratedDirectoryIsCurrent regenerates store and compares it
// with dir through requireGeneratedDirectoryMatches, or, under
// -update-docs (examples/docs_include_test.go's *updateDocs), writes store
// into dir instead of comparing.
func requireGeneratedDirectoryIsCurrent(t *testing.T, store generate.Store, dir string) {
	t.Helper()
	if *updateDocs {
		require.NoError(t, store.Write())
		return
	}
	requireGeneratedDirectoryMatches(t, store, dir)
}

// requireGeneratedDirectoryMatches is the comparison
// requireGeneratedDirectoryIsCurrent performs, and the one a caller whose
// directory -update-docs must never write into uses directly instead.
//
// It plans store, reads dir as it stands now, and requires the two to match
// through requireGeneratedFilesMatch. It also requires store.Plan to report
// no orphans, which is the same directory scan Store.Write's own orphan
// guard runs.
//
// A caller must not run a generator over dir before comparing: reading dir
// afterwards compares the generator with itself.
func requireGeneratedDirectoryMatches(t *testing.T, store generate.Store, dir string) {
	t.Helper()

	plan, err := store.Plan()
	require.NoError(t, err)

	requireGeneratedFilesMatch(t, plan, dir, generatedFilesIn(t, dir))

	orphans := plan.Orphans()
	require.Empty(t, orphans, "%s holds generated file(s) this store no longer writes: %v", dir, orphans)
}

// requireGeneratedFilesMatch requires present -- the generated files of one
// directory, keyed by base name -- to name the same set of files plan writes
// with the same bytes, reporting every difference staleGeneratedFiles found
// rather than stopping at the first. dir is used only to name the offending
// file in a message; present is what is actually compared, so a caller
// holding a snapshot of dir from before a generator ran can pass that
// instead of the directory's current contents.
func requireGeneratedFilesMatch(t *testing.T, plan generate.Plan, dir string, present map[string][]byte) {
	t.Helper()
	for _, problem := range staleGeneratedFiles(plan, dir, present) {
		t.Error(problem)
	}
}

// staleGeneratedFiles returns one message per way present differs from what
// plan writes: a file plan would write that present does not have gets its
// own message, a file present has that plan does not write gets its own
// message, and a shared name whose bytes differ gets its own message, so the
// three ways a directory can be stale are never collapsed into one. It
// returns the messages instead of failing so that
// TestStaleGeneratedFilesReportsEveryDifference can assert on them.
func staleGeneratedFiles(plan generate.Plan, dir string, present map[string][]byte) []string {
	planned := make(map[string][]byte)
	for _, f := range plan.Files() {
		planned[filepath.Base(f.Path)] = f.Source
	}

	var problems []string
	for _, name := range sortedKeys(planned) {
		if _, ok := present[name]; !ok {
			problems = append(problems, fmt.Sprintf("%s is generated and not checked in: %s", name, filepath.Join(dir, name)))
		}
	}
	for _, name := range sortedKeys(present) {
		if _, ok := planned[name]; !ok {
			problems = append(problems, fmt.Sprintf("%s is in the directory and no longer generated: %s", name, filepath.Join(dir, name)))
		}
	}
	for _, name := range sortedKeys(planned) {
		got, ok := present[name]
		if !ok {
			continue
		}
		if !bytes.Equal(planned[name], got) {
			problems = append(problems, fmt.Sprintf("%s is stale; run `go test ./examples/ -update-docs`: %s", name, filepath.Join(dir, name)))
		}
	}
	return problems
}

// generatedFilesIn reads dir directly with os.ReadDir, rather than through
// store.Plan's own directory scan, and returns the regular files in it that
// isGeneratedFile keeps, keyed by base name.
func generatedFilesIn(t *testing.T, dir string) map[string][]byte {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	present := make(map[string][]byte)
	for _, entry := range entries {
		if !isRegularFile(t, entry) {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, err)
		if !isGeneratedFile(entry.Name(), contents) {
			continue
		}
		present[entry.Name()] = contents
	}
	return present
}

// isRegularFile reports whether entry is a plain file, and is the one owner
// of that test for every directory scan in this package: generatedFilesIn,
// which decides what a plan is compared against, and snapshotDir and
// restoreDir, which have to capture and put back exactly the entries that
// comparison will see.
func isRegularFile(t *testing.T, entry os.DirEntry) bool {
	t.Helper()
	if entry.IsDir() {
		return false
	}
	info, err := entry.Info()
	require.NoError(t, err)
	return info.Mode().IsRegular()
}

// generatedFiles keeps from files -- a whole directory captured by
// snapshotDir, keyed by base name -- exactly the entries isGeneratedFile
// keeps, so a snapshot taken before a generator ran can be compared against
// a plan on the same terms generatedFilesIn compares a directory on.
func generatedFiles(files map[string][]byte) map[string][]byte {
	kept := make(map[string][]byte, len(files))
	for name, contents := range files {
		if !isGeneratedFile(name, contents) {
			continue
		}
		kept[name] = contents
	}
	return kept
}

// isGeneratedFile reports whether a file named name holding contents is
// rasqlgen's own output, and is the single owner of that rule for both
// generatedFilesIn and generatedFiles.
//
// The name must end in _gen.go or _gen_test.go, which is exactly the suffix
// set internal/genfile will ever write, and contents must open with
// genfile.Marker standing alone on its first line -- the same test
// generate/plan.go applies to an orphan candidate. Requiring the marker
// excludes a hand-written file that merely happens to share the suffix, such
// as sample/taskboard/internal/store/members_gen_test.go.
func isGeneratedFile(name string, contents []byte) bool {
	if !strings.HasSuffix(name, "_gen.go") && !strings.HasSuffix(name, "_gen_test.go") {
		return false
	}
	line, _, _ := bytes.Cut(contents, []byte("\n"))
	return string(bytes.TrimRight(line, "\r")) == genfile.Marker
}

// TestStaleGeneratedFilesReportsEveryDifference pins the three differences
// staleGeneratedFiles tells apart, so a caller that passes it the wrong set
// of files is failing against a
// comparison that is known to work rather than one that might not.
//
// It starts from the plan's own files as the present set, which is what a
// directory that is exactly current looks like, and changes one thing at a
// time.
func TestStaleGeneratedFilesReportsEveryDifference(t *testing.T) {
	dir := filepath.Join(repositoryRoot, "examples", "store")
	plan, err := exampleStore(t).Plan()
	require.NoError(t, err)

	current := func() map[string][]byte {
		files := make(map[string][]byte)
		for _, f := range plan.Files() {
			files[filepath.Base(f.Path)] = f.Source
		}
		return files
	}
	require.Contains(t, current(), generatedQuery, "the store no longer plans %s", generatedQuery)
	require.Empty(t, staleGeneratedFiles(plan, dir, current()),
		"a present set equal to the plan reported a difference")

	t.Run("bytes differ", func(t *testing.T) {
		present := current()
		present[generatedQuery] = append(append([]byte{}, present[generatedQuery]...), staleDriftLine...)
		require.Equal(t,
			[]string{generatedQuery + " is stale; run `go test ./examples/ -update-docs`: " + filepath.Join(dir, generatedQuery)},
			staleGeneratedFiles(plan, dir, present))
	})

	t.Run("planned file absent", func(t *testing.T) {
		present := current()
		delete(present, generatedQuery)
		require.Equal(t,
			[]string{generatedQuery + " is generated and not checked in: " + filepath.Join(dir, generatedQuery)},
			staleGeneratedFiles(plan, dir, present))
	})

	t.Run("present file no longer planned", func(t *testing.T) {
		// An invented name the store never plans, so this fixture can
		// never be mistaken for a file the repository really generates.
		const dropped = "invented_dropped_table_gen.go"
		present := current()
		present[dropped] = present[generatedQuery]
		require.Equal(t,
			[]string{dropped + " is in the directory and no longer generated: " + filepath.Join(dir, dropped)},
			staleGeneratedFiles(plan, dir, present))
	})
}

// TestGeneratedFilesKeepsWhatADirectoryScanKeeps requires generatedFiles, the
// filter a pre-generate snapshot goes through, to keep exactly what
// generatedFilesIn keeps when it scans the same directory. The two feed the
// same comparison, so a snapshot admitting a file the directory scan would
// drop -- or dropping one it would keep -- would make the checked-in bytes
// and the generator's output judged on different terms.
func TestGeneratedFilesKeepsWhatADirectoryScanKeeps(t *testing.T) {
	dir := filepath.Join(repositoryRoot, "examples", "store")

	whole := make(map[string][]byte)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, entry := range entries {
		if !isRegularFile(t, entry) {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, err)
		whole[entry.Name()] = contents
	}
	require.Greater(t, len(whole), len(generatedFiles(whole)),
		"%s holds no unmarked file, so this test would pass without the filter doing anything", dir)
	require.Equal(t, generatedFilesIn(t, dir), generatedFiles(whole))

	// A hand-written file that merely shares the suffix, invented here
	// rather than named from the tree, must be dropped by both.
	whole["invented_handwritten_gen.go"] = []byte("package store\n")
	require.NotContains(t, generatedFiles(whole), "invented_handwritten_gen.go")
}

// sortedKeys returns m's keys in sorted order, so a directory holding more
// than one stale file reports them in a fixed order rather than whatever a
// map happens to iterate in.
func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
