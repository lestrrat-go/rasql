package examples_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestSchemaSourceExampleGenerates runs the schemasource example through
// the very directive the documentation shows, `//go:generate go run ./gen`
// in the schema package, rather than compiling the program or calling
// generate.Store directly. Only running it that way exercises the working
// directory the directive gives it, which is what decides whether the
// relative output path in gen/main.go resolves at all.
//
// It then checks every file the directive wrote -- not just users_gen.go
// -- against a generate.Store built from the same table
// schemasource.Tables() returns, through requireGeneratedDirectoryMatches,
// so drift in schema_gen.go or schema_gen_test.go is caught instead of
// being silently rewritten by the directive that just ran. Because that
// directive rewrites its output files unconditionally on every run, this
// test snapshots the directory first and restores it afterward, so a stale
// example never leaves the working tree holding output this test did not
// check in.
//
// The snapshot is also what the checked-in bytes are judged by. Once
// `go generate` has run, the directory holds the directive's own fresh
// output, so comparing the directory against the store's plan only pins the
// directive to the library and says nothing about what is committed: a
// checked-in file that has drifted would be overwritten before the
// comparison and put back by the restore, leaving the drift in the tree and
// the test green. The pre-generate snapshot is therefore compared against
// the same plan through requireGeneratedFilesMatch, which is what fails, and
// names the file, when the committed source is stale.
//
// The restore is registered before `go generate` runs and before any
// requirement that can stop the test, because a directive that fails after
// it has already replaced a file would otherwise leave that file behind: a
// failed require exits the test through runtime.Goexit, and a defer
// registered further down never runs. -update-docs is the one mode with no
// restore, and no staleness comparison, since rewriting the checked-in
// directory is what that flag is for.
func TestSchemaSourceExampleGenerates(t *testing.T) {
	dir := filepath.Join(repositoryRoot, "examples", "schemasource", "internal", "store")
	before := snapshotDir(t, dir)
	if !*updateDocs {
		defer restoreDir(t, dir, before)
	}

	// No Dir: `go test` runs this package with its own directory as the
	// working directory, so ./schemasource is the example package.
	command := exec.CommandContext(t.Context(), "go", "generate", "./schemasource")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "go generate ./schemasource: %s", output)

	after, err := os.ReadFile(filepath.Join(dir, "users_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(after), "func Users() UsersTable {")

	users, err := schema.NewTableDef("users",
		schema.Integer("id"),
		schema.Text("email", schema.Width(255)),
		schema.PrimaryKey("id"),
	)
	require.NoError(t, err)

	store := generate.Store{
		Package: "store",
		Root:    repositoryRootAbs(t),
		Dir:     filepath.Join("examples", "schemasource", "internal", "store"),
		Tables:  []schema.TableDef{users},
	}

	if *updateDocs {
		requireGeneratedDirectoryIsCurrent(t, store, dir)
		return
	}

	plan, err := store.Plan()
	require.NoError(t, err)
	requireGeneratedFilesMatch(t, plan, dir, generatedFiles(before))

	requireGeneratedDirectoryMatches(t, store, dir)
}

// snapshotDir reads every regular file directly inside dir and returns its
// name and bytes, so restoreDir can put back exactly what a subprocess
// like `go generate` overwrote and TestSchemaSourceExampleGenerates can
// compare the checked-in bytes with the store's plan after the directive has
// replaced them on disk.
func snapshotDir(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	snapshot := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		if !isRegularFile(t, entry) {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, err)
		snapshot[entry.Name()] = contents
	}
	return snapshot
}

// restoreDir returns dir to exactly what snapshotDir captured, so a
// directive that rewrites its output on every run never leaves the working
// tree holding output this test did not check in, whether or not the
// comparison that follows it passes.
//
// It removes every regular file dir now holds that the snapshot does not,
// then writes the snapshot back. The removal is what puts back a directory
// the directive added a file to: generate.Store writes one <table>_gen.go per
// table, so adding a table to the example makes `go generate` create a file
// no snapshot entry can overwrite, and restoring only the saved files would
// leave that new file in the checked-in directory. It skips whatever
// snapshotDir skipped, through the same isRegularFile test, so an entry the
// snapshot never captured is never the one this deletes.
//
// It reports failures with t.Errorf rather than a require, because it runs
// from a defer that may already be unwinding a failed test and every
// remaining entry still has to be put back.
func restoreDir(t *testing.T, dir string, snapshot map[string][]byte) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Errorf("restore %s: %v", dir, err)
		return
	}
	for _, entry := range entries {
		if !isRegularFile(t, entry) {
			continue
		}
		if _, kept := snapshot[entry.Name()]; kept {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := os.Remove(path); err != nil {
			t.Errorf("remove generated %s: %v", path, err)
		}
	}
	for name, contents := range snapshot {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, contents, 0o644); err != nil {
			t.Errorf("restore %s: %v", path, err)
		}
	}
}

// staleDriftLine is an invented line, present nowhere in the tree, that
// TestSchemaSourceExampleGeneratesFailsOnStaleCheckedInFile appends to a
// checked-in generated file to make it stale. It is invented rather than
// copied from a real defect so that a later sweep for a genuinely bad line
// never matches this fixture.
const staleDriftLine = "\n// invented drift line, written only by a test that expects this file to be reported stale\n"

// TestSchemaSourceExampleGeneratesFailsOnStaleCheckedInFile is the
// regression test for TestSchemaSourceExampleGenerates judging the wrong
// bytes. It makes the checked-in users_gen.go stale, runs that test alone in
// a subprocess, and requires it to fail naming that file.
//
// Only a subprocess can prove this. The defect was in which bytes the test
// hands to the comparison, not in the comparison itself: `go generate`
// rewrites users_gen.go before any check runs, so a test that reads the
// directory afterwards passes on a stale tree no matter how strict its
// comparison is. Calling the comparison directly from here would reproduce
// the comparison and not the mistake.
//
// It also requires the drift to survive the subprocess, which is what shows
// the restore is still putting the checked-in bytes back rather than leaving
// the generator's fresh output in the tree.
func TestSchemaSourceExampleGeneratesFailsOnStaleCheckedInFile(t *testing.T) {
	if *updateDocs {
		t.Skip("-update-docs rewrites the checked-in directory, so there is no stale state to detect")
	}

	path := filepath.Join(repositoryRoot, "examples", "schemasource", "internal", "store", "users_gen.go")
	committed, err := os.ReadFile(path)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := os.WriteFile(path, committed, 0o644); err != nil {
			t.Errorf("restore %s: %v", path, err)
		}
	})

	stale := string(committed) + staleDriftLine
	require.NoError(t, os.WriteFile(path, []byte(stale), 0o644))

	// "." is this package: `go test` runs it with its own directory as the
	// working directory, the same reason TestSchemaSourceExampleGenerates
	// names ./schemasource without a Dir. -count=1 keeps a cached pass from
	// standing in for a run against the stale file.
	command := exec.CommandContext(t.Context(), "go", "test", ".",
		"-run", "^TestSchemaSourceExampleGenerates$", "-count=1")
	output, err := command.CombinedOutput()
	require.Error(t, err, "TestSchemaSourceExampleGenerates passed against a stale users_gen.go:\n%s", output)
	require.Contains(t, string(output), "users_gen.go is stale", "the failure did not name the stale file:\n%s", output)

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, stale, string(after),
		"the subprocess left the generator's own output in the tree instead of restoring the checked-in bytes")
}
