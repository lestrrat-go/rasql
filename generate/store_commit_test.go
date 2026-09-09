// This file is package generate, not generate_test, because
// TestPlanCommitWritesInThePlannedOrder swaps writeGeneratedFile and
// removeGeneratedFile to record the order Plan.Commit calls them in. Both
// are unexported package-level seams, so only a test inside this package
// can reach them -- cli/rasqlgen/rasqlgen_test.go does the same thing for
// openDatabase. The helper table definitions below are local to this file,
// duplicated from generate_test's write_test.go, because that file lives
// in the separate generate_test package and its unexported helpers are not
// reachable from here.
package generate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func commitTestUsersDef() schema.TableDef {
	return schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
	)
}

func commitTestOrdersDef() schema.TableDef {
	return schema.MustTableDef("orders",
		schema.Integer("id"),
		schema.Integer("user_id"),
		schema.PrimaryKey("id"),
	)
}

// commitTestNamedDef returns a table shaped like commitTestUsersDef under
// whatever name the caller gives, which is what the case-only rename test
// below varies.
func commitTestNamedDef(name string) schema.TableDef {
	return schema.MustTableDef(name,
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
	)
}

// snapshotDirFiles reads every regular file directly in dir into a
// name->content map, for a before/after comparison that proves a call
// touched nothing.
func snapshotDirFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	files := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, err)
		files[entry.Name()] = data
	}
	return files
}

// TestStoreWriteCreatesAMissingDirectory confirms Write creates Dir, and every
// missing parent above it.
func TestStoreWriteCreatesAMissingDirectory(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "missing", "parent", "store")
	require.NoDirExists(t, dir)

	store := compactStore(t, dir, commitTestUsersDef())
	require.NoError(t, store.Write())

	require.DirExists(t, dir)
	require.FileExists(t, filepath.Join(dir, "users_gen.go"))
	require.FileExists(t, filepath.Join(dir, "schema_gen.go"))
	require.FileExists(t, filepath.Join(dir, "schema_gen_test.go"))
}

// TestPlanCommitWritesInThePlannedOrder swaps writeGeneratedFile and
// removeGeneratedFile for recording stand-ins and checks the sequence
// Commit calls them in: every per-table and query file first (in path
// order), then every deletion, then schema_gen.go, then
// schema_gen_test.go.
func TestPlanCommitWritesInThePlannedOrder(t *testing.T) {
	dir := t.TempDir()
	users := commitTestUsersDef()
	require.NoError(t, compactStore(t, dir, users).Write())

	orphan := filepath.Join(dir, "dropped_gen.go")
	require.NoError(t, os.WriteFile(orphan, []byte(genfile.Marker+"\n\npackage store\n"), 0o600))

	// A query file is part of what this test orders, so the store carries one
	// alongside its table.
	store := pruningStore(t, dir, users)
	store.TypedQueries = []TypedQuery{publicationTestQuery()}
	plan, err := store.Plan()
	require.NoError(t, err)
	require.Equal(t, []string{orphan}, plan.Orphans())

	var sequence []string
	origWrite, origRemove := writeGeneratedFile, removeGeneratedFile
	t.Cleanup(func() { writeGeneratedFile, removeGeneratedFile = origWrite, origRemove })
	writeGeneratedFile = func(dir *os.Root, name string, source []byte) error {
		sequence = append(sequence, "write:"+name)
		return origWrite(dir, name, source)
	}
	removeGeneratedFile = func(dir *os.Root, name string) error {
		sequence = append(sequence, "delete:"+name)
		return origRemove(dir, name)
	}

	require.NoError(t, plan.Commit())
	require.Equal(t, []string{
		"write:q_gen.go",
		"write:users_gen.go",
		"delete:dropped_gen.go",
		"write:schema_gen.go",
		"write:schema_gen_test.go",
	}, sequence)
}

// TestPlanCommitWritesNothingWhenADestinationIsRefused plans into a
// directory that does not hold users_gen.go yet -- so Plan itself succeeds
// -- then puts a hand-written users_gen.go carrying no marker in the way
// before Commit runs. Commit's own destination check, freshly re-resolved
// rather than trusted from Plan time, must refuse the run, and nothing
// else the plan would have written may exist afterwards; the hand-written
// file itself must be untouched.
func TestPlanCommitWritesNothingWhenADestinationIsRefused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	store := compactStore(t, dir, commitTestUsersDef(), commitTestOrdersDef())
	plan, err := store.Plan()
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(dir, 0o700))
	handWritten := []byte("package store\n\n// hand-written, no marker\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "users_gen.go"), handWritten, 0o600))

	err = plan.Commit()
	require.Error(t, err)

	require.NoFileExists(t, filepath.Join(dir, "orders_gen.go"))
	require.NoFileExists(t, filepath.Join(dir, "schema_gen.go"))
	require.NoFileExists(t, filepath.Join(dir, "schema_gen_test.go"))
	got, err := os.ReadFile(filepath.Join(dir, "users_gen.go"))
	require.NoError(t, err)
	require.Equal(t, handWritten, got)
}

func TestHeldStorePlanRefusesNewGeneratedOutput(t *testing.T) {
	moduleDir := t.TempDir()
	repoGoMod, err := os.ReadFile(filepath.Join("..", "go.mod"))
	require.NoError(t, err)
	repository, err := filepath.Abs("..")
	require.NoError(t, err)
	module := strings.Replace(string(repoGoMod), "module github.com/lestrrat-go/rasql\n", "module example.com/consumer\n", 1)
	module += "\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte(module), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "go.sum"), mustReadFile(t, filepath.Join("..", "go.sum")), 0o600))

	dir := filepath.Join(moduleDir, "internal", "store")
	users, orders := commitTestUsersDef(), commitTestOrdersDef()
	held, err := (compactStore(t, dir, users)).Plan()
	require.NoError(t, err)
	require.Empty(t, held.Orphans())
	require.NoError(t, (compactStore(t, dir, users, orders)).Write())
	runGeneratedPackageTest(t, moduleDir)
	fresh, err := (compactStore(t, dir, users)).Plan()
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(dir, "orders_gen.go")}, fresh.Orphans())

	before := snapshotDirFiles(t, dir)
	err = held.Commit()
	require.Error(t, err)
	require.ErrorContains(t, err, "gained 1 file(s) rasqlgen wrote that this plan does not write")
	require.ErrorContains(t, err, "orders_gen.go")
	require.ErrorContains(t, err, "rerun Store.Plan")
	require.NotErrorIs(t, err, ErrStale)
	require.Equal(t, before, snapshotDirFiles(t, dir))
	runGeneratedPackageTest(t, moduleDir)

	err = held.Check()
	require.Error(t, err)
	require.ErrorContains(t, err, "gained 1 file(s) rasqlgen wrote that this plan does not write")
	require.ErrorContains(t, err, "orders_gen.go")
	require.ErrorContains(t, err, "rerun Store.Plan")
	require.NotErrorIs(t, err, ErrStale)
	require.Equal(t, before, snapshotDirFiles(t, dir))
}

func TestHeldStorePlanAllowsHandwrittenSamePackageFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "helper.go"), []byte("package store\n"), 0o600))
	plan, err := (compactStore(t, dir, commitTestUsersDef())).Plan()
	require.NoError(t, err)
	require.NoError(t, plan.Commit())
	require.NoError(t, plan.Check())
}

func TestHeldStorePlanRefusesNewForeignPackageFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	store := compactStore(t, dir, commitTestUsersDef())
	require.NoError(t, store.Write())
	plan, err := store.Plan()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "foreign.go"), []byte("package other\n"), 0o600))
	before := snapshotDirFiles(t, dir)

	err = plan.Check()
	require.Error(t, err)
	require.ErrorContains(t, err, `already holds package "other"`)
	require.Equal(t, before, snapshotDirFiles(t, dir))
	err = plan.Commit()
	require.Error(t, err)
	require.ErrorContains(t, err, `already holds package "other"`)
	require.Equal(t, before, snapshotDirFiles(t, dir))
}

func TestHeldStorePlanWithPruneRefusesNewGeneratedOutput(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	users, orders := commitTestUsersDef(), commitTestOrdersDef()
	held, err := (pruningStore(t, dir, users)).Plan()
	require.NoError(t, err)
	require.NoError(t, (compactStore(t, dir, users, orders)).Write())
	before := snapshotDirFiles(t, dir)

	err = held.Commit()
	require.ErrorContains(t, err, "gained 1 file(s) rasqlgen wrote")
	require.Equal(t, before, snapshotDirFiles(t, dir))
	err = held.Check()
	require.ErrorContains(t, err, "gained 1 file(s) rasqlgen wrote")
	require.Equal(t, before, snapshotDirFiles(t, dir))
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func runGeneratedPackageTest(t *testing.T, moduleDir string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "go", "test", "./...")
	command.Dir = moduleDir
	command.Env = append(os.Environ(), "GOPROXY=off")
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "go test output:\n%s", output)
	require.NotContainsf(t, string(output), "go: downloading", "go test output:\n%s", output)
}

// TestPlanCommitRefusesLeftoversWithoutPrune confirms a marker-carrying
// leftover makes Commit fail, naming it, before writing or deleting
// anything at all.
func TestPlanCommitRefusesLeftoversWithoutPrune(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, compactStore(t, dir, commitTestUsersDef()).Write())
	orphan := filepath.Join(dir, "dropped_gen.go")
	require.NoError(t, os.WriteFile(orphan, []byte(genfile.Marker+"\n\npackage store\n"), 0o600))
	before := snapshotDirFiles(t, dir)

	store := compactStore(t, dir, commitTestUsersDef())
	plan, err := store.Plan()
	require.NoError(t, err)
	require.Equal(t, []string{orphan}, plan.Orphans())

	err = plan.Commit()
	require.ErrorContains(t, err, orphan)
	require.Equal(t, before, snapshotDirFiles(t, dir))
}

// TestPlanCommitPrunesLeftoversWithPrune confirms Prune lets Commit delete
// a recorded orphan and still land every planned file.
func TestPlanCommitPrunesLeftoversWithPrune(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, compactStore(t, dir, commitTestUsersDef()).Write())
	orphan := filepath.Join(dir, "dropped_gen.go")
	require.NoError(t, os.WriteFile(orphan, []byte(genfile.Marker+"\n\npackage store\n"), 0o600))

	store := pruningStore(t, dir, commitTestUsersDef())
	plan, err := store.Plan()
	require.NoError(t, err)
	require.Equal(t, []string{orphan}, plan.Orphans())

	require.NoError(t, plan.Commit())
	require.NoFileExists(t, orphan)
	for _, f := range plan.Files() {
		require.FileExists(t, f.Path)
	}
}

// TestPlanCommitRefusesToDeleteAFileThatLostItsMarker rewrites a recorded
// orphan's first line between Plan and Commit, so it no longer carries
// rasqlgen's marker. Commit must refuse rather than delete it, and must
// delete nothing else either.
func TestPlanCommitRefusesToDeleteAFileThatLostItsMarker(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, compactStore(t, dir, commitTestUsersDef()).Write())
	orphan := filepath.Join(dir, "dropped_gen.go")
	require.NoError(t, os.WriteFile(orphan, []byte(genfile.Marker+"\n\npackage store\n"), 0o600))

	store := pruningStore(t, dir, commitTestUsersDef())
	plan, err := store.Plan()
	require.NoError(t, err)
	require.Equal(t, []string{orphan}, plan.Orphans())

	require.NoError(t, os.WriteFile(orphan, []byte("package store\n\n// no longer generated by rasqlgen\n"), 0o600))
	before := snapshotDirFiles(t, dir)

	err = plan.Commit()
	require.ErrorContains(t, err, orphan)
	require.Equal(t, before, snapshotDirFiles(t, dir))
}

// TestPlanCommitDeletesNothingItWrote covers the case-only rename
// invariant 3 in Plan's own doc comment: a store previously generated for
// "Users" and now generated for "users" shares one destination file, so
// Commit, even with Prune set, must leave exactly one users_gen.go rather
// than writing it and then deleting it as an orphan.
func TestPlanCommitDeletesNothingItWrote(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, compactStore(t, dir, commitTestNamedDef("Users")).Write())

	store := pruningStore(t, dir, commitTestNamedDef("users"))
	plan, err := store.Plan()
	require.NoError(t, err)
	require.Empty(t, plan.Orphans(), "the file already on disk for Users and the file planned for users share one destination")

	require.NoError(t, plan.Commit())
	require.FileExists(t, filepath.Join(dir, "users_gen.go"))
}

// TestPlanCommitIsIdempotent confirms committing the same Plan value twice
// leaves identical bytes on disk.
func TestPlanCommitIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	store := compactStore(t, dir, commitTestUsersDef(), commitTestOrdersDef())
	plan, err := store.Plan()
	require.NoError(t, err)
	require.NoError(t, plan.Commit())
	before := snapshotDirFiles(t, dir)

	require.NoError(t, plan.Commit())
	require.Equal(t, before, snapshotDirFiles(t, dir))
}

// TestZeroPlanCommitErrors confirms Plan{}.Commit reports an error naming
// Store.Plan as the only thing that builds a Plan Commit can act on.
func TestZeroPlanCommitErrors(t *testing.T) {
	var p Plan
	err := p.Commit()
	require.ErrorContains(t, err, "Store.Plan")
}
