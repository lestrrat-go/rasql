package examples_test

import (
	"bytes"
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

// TestGeneratedStoreIsCurrent regenerates the checked-in examples/store
// package -- the per-table file, the two descriptor files, and the one
// compiled query -- through generate.Store, the same path a user's own
// gen/main.go runs, and fails when the directory differs from what that
// store plans. The definition and the query are stated here in Go rather
// than read from a snapshot file, so the check needs neither a database
// nor a checked-in copy of the schema.
func TestGeneratedStoreIsCurrent(t *testing.T) {
	users, err := schema.NewTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
	)
	require.NoError(t, err)

	store := generate.Store{
		Package: "store",
		Root:    repositoryRootAbs(t),
		Dir:     filepath.Join("examples", "store"),
		Tables:  []schema.TableDef{users},
		Queries: []generate.Query{{
			Input:    filepath.Join("examples", "store", "user_by_email.sql"),
			Function: "UserByEmail",
			Output:   "user_by_email_gen.go",
			Dialect:  dialect.PostgreSQL(),
		}},
	}

	requireGeneratedDirectoryIsCurrent(t, store, filepath.Join(repositoryRoot, "examples", "store"))
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
// directory -update-docs must never write into (TestTaskboardStoreIsCurrent)
// uses directly instead.
//
// It plans store, reads dir, and requires the two to name the same set of
// generated files with the same bytes: a file store.Plan would write that
// dir does not have fails on its own, a file dir has that store.Plan does
// not plan fails on its own, and a shared name whose bytes differ fails on
// its own, so the three ways a directory can be stale are never collapsed
// into one message. It also requires store.Plan to report no orphans,
// which is the same directory scan Store.Write's own orphan guard runs.
//
// dir is read directly with os.ReadDir rather than through store.Plan's
// own directory scan, keeping every regular file whose name ends in
// _gen.go or _gen_test.go and whose first line is genfile.Marker: that is
// exactly the suffix set internal/genfile will ever write, and requiring
// the marker excludes a hand-written file that merely happens to share the
// suffix, such as sample/taskboard/internal/store/members_gen_test.go.
func requireGeneratedDirectoryMatches(t *testing.T, store generate.Store, dir string) {
	t.Helper()

	plan, err := store.Plan()
	require.NoError(t, err)

	planned := make(map[string][]byte)
	for _, f := range plan.Files() {
		planned[filepath.Base(f.Path)] = f.Source
	}

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	present := make(map[string][]byte)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, "_gen.go") && !strings.HasSuffix(name, "_gen_test.go") {
			continue
		}
		info, err := entry.Info()
		require.NoError(t, err)
		if !info.Mode().IsRegular() {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		if !hasGenfileMarker(contents) {
			continue
		}
		present[name] = contents
	}

	for _, name := range sortedKeys(planned) {
		if _, ok := present[name]; !ok {
			t.Errorf("%s is generated and not checked in: %s", name, filepath.Join(dir, name))
		}
	}
	for _, name := range sortedKeys(present) {
		if _, ok := planned[name]; !ok {
			t.Errorf("%s is in the directory and no longer generated: %s", name, filepath.Join(dir, name))
		}
	}
	for _, name := range sortedKeys(planned) {
		got, ok := present[name]
		if !ok {
			continue
		}
		if !bytes.Equal(planned[name], got) {
			t.Errorf("%s is stale; run `go test ./examples/ -update-docs`: %s", name, filepath.Join(dir, name))
		}
	}

	orphans := plan.Orphans()
	require.Empty(t, orphans, "%s holds generated file(s) this store no longer writes: %v", dir, orphans)
}

// hasGenfileMarker reports whether contents opens with genfile.Marker
// standing alone on its first line, the same test generate/plan.go applies
// to an orphan candidate before treating it as rasqlgen's own output.
func hasGenfileMarker(contents []byte) bool {
	line, _, _ := bytes.Cut(contents, []byte("\n"))
	return string(bytes.TrimRight(line, "\r")) == genfile.Marker
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
