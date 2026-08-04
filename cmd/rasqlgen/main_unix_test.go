//go:build unix

package main

import (
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// The tests below inject a write failure with RLIMIT_FSIZE and SIGXFSZ,
// assert Unix permission bits, and create symbolic links. Neither the
// resource-limit API nor the signal exists outside Unix, and creating a
// symbolic link on Windows needs a privilege an ordinary test run does not
// have, so this file is guarded by //go:build unix. A runtime GOOS check
// cannot stand in for the guard, because the test package fails to compile
// for a non-Unix target before any test runs.

// TestRunSchemaPreservesExistingOutputWhenWriteFails proves that a run
// which fails partway through writing the generated file does not clobber
// a pre-existing output file. Duplicate table names (the fixture used by
// TestRunSchemaRejectsDuplicateFilteredInputTables) are rejected by
// generate.Schema before any file is opened, so that fixture cannot
// exercise the truncate-then-fail defect this test targets: it never
// reaches the write step under either the old or the new code. This test
// instead uses a valid schema and forces the write itself to fail
// partway, by lowering RLIMIT_FSIZE for the duration of the call, which is
// what actually reproduces "truncation happens at open, before any byte is
// written."
func TestRunSchemaPreservesExistingOutputWhenWriteFails(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	input := filepath.Join(directory, "schema.json")
	output := filepath.Join(directory, "schema.go")
	data := []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":"integer"}],"PrimaryKey":["id"]}]`)
	require.NoError(t, os.WriteFile(input, data, 0o600))
	sentinel := []byte("SENTINEL-DATA-DO-NOT-TRUNCATE")
	require.NoError(t, os.WriteFile(output, sentinel, 0o600))

	// Catching SIGXFSZ turns an over-limit write into an EFBIG error
	// instead of the default action, which terminates the process.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGXFSZ)
	t.Cleanup(func() {
		signal.Stop(signals)
	})

	var limit syscall.Rlimit
	require.NoError(t, syscall.Getrlimit(syscall.RLIMIT_FSIZE, &limit))
	original := limit
	limit.Cur = 1
	require.NoError(t, syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limit))

	runErr := run([]string{"schema", "-input", input, "-package", "generated", "-output", output})

	require.NoError(t, syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original))

	require.Error(t, runErr)
	got, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, sentinel, got)
}

// TestRunSchemaKeepsOutputPermissionBits pins the mode of the generated
// file. Writing through a temporary file makes the output's mode come from
// the temporary file rather than from the destination, so regenerating over
// a file the user created at 0644 would silently narrow it to 0600 unless
// writeGeneratedFile copies the destination's bits across first. A path
// that does not exist yet still gets 0600.
func TestRunSchemaKeepsOutputPermissionBits(t *testing.T) {
	data := []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":"integer"}],"PrimaryKey":["id"]}]`)
	for _, tc := range []struct {
		name     string
		existing fs.FileMode
		want     fs.FileMode
	}{
		{name: "new output is created at 0600", want: 0o600},
		{name: "existing 0644 output keeps 0644", existing: 0o644, want: 0o644},
		{name: "existing 0640 output keeps 0640", existing: 0o640, want: 0o640},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, os.RemoveAll(directory))
			})
			input := filepath.Join(directory, "schema.json")
			output := filepath.Join(directory, "schema.go")
			require.NoError(t, os.WriteFile(input, data, 0o600))
			if tc.existing != 0 {
				require.NoError(t, os.WriteFile(output, []byte("// stale\n"), tc.existing))
				// os.WriteFile masks perm with the umask, so set the
				// bits explicitly to keep the fixture deterministic.
				require.NoError(t, os.Chmod(output, tc.existing))
			}

			require.NoError(t, run([]string{"schema", "-input", input, "-package", "generated", "-output", output}))

			info, err := os.Stat(output)
			require.NoError(t, err)
			require.Equal(t, tc.want, info.Mode().Perm())
		})
	}
}

// schemaFixtureInput writes the schema JSON every test in this file
// generates from into directory and returns its path.
func schemaFixtureInput(t *testing.T, directory string) string {
	t.Helper()
	input := filepath.Join(directory, "schema.json")
	data := []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":"integer"}],"PrimaryKey":["id"]}]`)
	require.NoError(t, os.WriteFile(input, data, 0o600))
	return input
}

// TestRunSchemaWritesThroughOutputSymlink pins what happens when -output
// names a symbolic link. Renaming a temporary file over the link's own path
// would replace the link with a regular file and leave the file it pointed
// at holding stale content, while the run still reported success, so the
// write has to resolve the link first and replace its target instead.
func TestRunSchemaWritesThroughOutputSymlink(t *testing.T) {
	t.Run("link to a file in the same directory keeps the link and updates its target", func(t *testing.T) {
		directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, os.RemoveAll(directory))
		})
		input := schemaFixtureInput(t, directory)
		target := filepath.Join(directory, "target.go")
		output := filepath.Join(directory, "output.go")
		require.NoError(t, os.WriteFile(target, []byte("SENTINEL-DATA"), 0o644))
		// os.WriteFile masks perm with the umask, so set the bits
		// explicitly to keep the fixture deterministic.
		require.NoError(t, os.Chmod(target, 0o644))
		require.NoError(t, os.Symlink("target.go", output))

		require.NoError(t, run([]string{"schema", "-input", input, "-package", "generated", "-output", output}))

		link, err := os.Lstat(output)
		require.NoError(t, err)
		require.NotZero(t, link.Mode()&fs.ModeSymlink, "output must still be a symbolic link")
		destination, err := os.Readlink(output)
		require.NoError(t, err)
		require.Equal(t, "target.go", destination)
		source, err := os.ReadFile(target)
		require.NoError(t, err)
		require.Contains(t, string(source), "func Users() UsersTable {")
		info, err := os.Stat(target)
		require.NoError(t, err)
		require.Equal(t, fs.FileMode(0o644), info.Mode().Perm())

		// The temporary file belongs to the target's directory, which is
		// this one, and the rename must have consumed it.
		entries, err := os.ReadDir(directory)
		require.NoError(t, err)
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		require.ElementsMatch(t, []string{"schema.json", "target.go", "output.go"}, names)
	})

	t.Run("link to a file in another directory puts the temporary file beside the target", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permission bits, so the read-only link directory proves nothing")
		}
		root, err := os.MkdirTemp(".", ".tmp-schema-command-*")
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, os.RemoveAll(root))
		})
		linkDirectory := filepath.Join(root, "link")
		targetDirectory := filepath.Join(root, "target")
		require.NoError(t, os.Mkdir(linkDirectory, 0o700))
		require.NoError(t, os.Mkdir(targetDirectory, 0o700))
		input := schemaFixtureInput(t, root)
		target := filepath.Join(targetDirectory, "target.go")
		output := filepath.Join(linkDirectory, "output.go")
		require.NoError(t, os.WriteFile(target, []byte("SENTINEL-DATA"), 0o600))
		require.NoError(t, os.Symlink(filepath.Join("..", "target", "target.go"), output))
		// Denying writes to the directory holding the link makes this a
		// mechanical check rather than an inspection of the temporary
		// file's name: a temporary file created beside the link instead
		// of beside its target cannot be created at all, so the run
		// fails. Restore the bits before the RemoveAll cleanup, which
		// registered first and therefore runs last.
		require.NoError(t, os.Chmod(linkDirectory, 0o500))
		t.Cleanup(func() {
			require.NoError(t, os.Chmod(linkDirectory, 0o700))
		})

		require.NoError(t, run([]string{"schema", "-input", input, "-package", "generated", "-output", output}))

		link, err := os.Lstat(output)
		require.NoError(t, err)
		require.NotZero(t, link.Mode()&fs.ModeSymlink, "output must still be a symbolic link")
		source, err := os.ReadFile(target)
		require.NoError(t, err)
		require.Contains(t, string(source), "func Users() UsersTable {")
	})

	t.Run("link to a path that does not exist creates the target at 0600", func(t *testing.T) {
		directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, os.RemoveAll(directory))
		})
		input := schemaFixtureInput(t, directory)
		target := filepath.Join(directory, "target.go")
		output := filepath.Join(directory, "output.go")
		require.NoError(t, os.Symlink("target.go", output))

		require.NoError(t, run([]string{"schema", "-input", input, "-package", "generated", "-output", output}))

		link, err := os.Lstat(output)
		require.NoError(t, err)
		require.NotZero(t, link.Mode()&fs.ModeSymlink, "output must still be a symbolic link")
		source, err := os.ReadFile(target)
		require.NoError(t, err)
		require.Contains(t, string(source), "func Users() UsersTable {")
		info, err := os.Stat(target)
		require.NoError(t, err)
		require.Equal(t, fs.FileMode(0o600), info.Mode().Perm())
	})

	t.Run("link that points at itself fails without touching anything", func(t *testing.T) {
		directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, os.RemoveAll(directory))
		})
		input := schemaFixtureInput(t, directory)
		output := filepath.Join(directory, "output.go")
		require.NoError(t, os.Symlink("output.go", output))

		err = run([]string{"schema", "-input", input, "-package", "generated", "-output", output})
		require.Error(t, err)
		require.Contains(t, err.Error(), "too many levels of symbolic links")

		link, err := os.Lstat(output)
		require.NoError(t, err)
		require.NotZero(t, link.Mode()&fs.ModeSymlink, "output must still be a symbolic link")
		entries, err := os.ReadDir(directory)
		require.NoError(t, err)
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		require.ElementsMatch(t, []string{"schema.json", "output.go"}, names)
	})

	t.Run("two links pointing at each other fail without touching anything", func(t *testing.T) {
		directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, os.RemoveAll(directory))
		})
		input := schemaFixtureInput(t, directory)
		output := filepath.Join(directory, "output.go")
		other := filepath.Join(directory, "other.go")
		require.NoError(t, os.Symlink("other.go", output))
		require.NoError(t, os.Symlink("output.go", other))

		err = run([]string{"schema", "-input", input, "-package", "generated", "-output", output})
		require.Error(t, err)
		require.Contains(t, err.Error(), "too many levels of symbolic links")
	})
}
