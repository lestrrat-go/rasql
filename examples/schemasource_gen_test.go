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
// The restore is registered before `go generate` runs and before any
// requirement that can stop the test, because a directive that fails after
// it has already replaced a file would otherwise leave that file behind: a
// failed require exits the test through runtime.Goexit, and a defer
// registered further down never runs. -update-docs is the one mode with no
// restore, since rewriting the checked-in directory is what that flag is
// for.
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
	requireGeneratedDirectoryMatches(t, store, dir)
}

// snapshotDir reads every regular file directly inside dir and returns its
// name and bytes, so restoreDir can put back exactly what a subprocess
// like `go generate` overwrote.
func snapshotDir(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	snapshot := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
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
// It removes every file dir now holds that the snapshot does not, then
// writes the snapshot back. The removal is what puts back a directory the
// directive added a file to: generate.Store writes one <table>_gen.go per
// table, so adding a table to the example makes `go generate` create a file
// no snapshot entry can overwrite, and restoring only the saved files would
// leave that new file in the checked-in directory.
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
		if entry.IsDir() {
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
