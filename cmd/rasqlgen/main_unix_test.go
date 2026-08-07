//go:build unix

package main

import (
	"fmt"
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
	output := filepath.Join(directory, "schema_gen.go")
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

// TestRunSchemaKeepsOutputModeBits pins the mode of the generated file.
// Writing through a temporary file makes the output's mode come from the
// temporary file rather than from the destination, so regenerating over a
// file the user created at 0644 would silently narrow it to 0600 unless
// writeGeneratedFile copies the destination's bits across first. A path
// that does not exist yet still gets 0600.
//
// The sticky bit is part of what an existing file keeps, because an
// in-place write kept it. Setuid and setgid are not: Linux clears both when
// an unprivileged process writes a file, so an in-place write dropped them
// as well, and the cases below pin that this command does not hand them
// back.
func TestRunSchemaKeepsOutputModeBits(t *testing.T) {
	data := []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":"integer"}],"PrimaryKey":["id"]}]`)
	for _, tc := range []struct {
		name     string
		existing fs.FileMode
		want     fs.FileMode
	}{
		{name: "new output is created at 0600", want: 0o600},
		{name: "existing 0644 output keeps 0644", existing: 0o644, want: 0o644},
		{name: "existing 0640 output keeps 0640", existing: 0o640, want: 0o640},
		{name: "existing sticky output keeps the sticky bit", existing: 0o755 | fs.ModeSticky, want: 0o755 | fs.ModeSticky},
		{name: "existing setuid output does not keep setuid", existing: 0o755 | fs.ModeSetuid, want: 0o755},
		{name: "existing setgid output does not keep setgid", existing: 0o755 | fs.ModeSetgid, want: 0o755},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, os.RemoveAll(directory))
			})
			input := filepath.Join(directory, "schema.json")
			output := filepath.Join(directory, "schema_gen.go")
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
			require.Equal(t, tc.want, info.Mode()&(fs.ModePerm|fs.ModeSticky|fs.ModeSetuid|fs.ModeSetgid))
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

// TestRunRejectsSymlinkToDirectoryOutput extends the directory-rejection
// fix in TestRunRejectsDirectoryOutput (main_test.go) to a symlink whose
// target is a directory. resolveOutputPath only sees that target after
// following the link, so this exercises a later iteration of its loop than
// the immediate os.Lstat check an ordinary directory path hits, and it must
// reject the link the same way, with or without a trailing slash, leaving
// nothing behind at the link's target.
func TestRunRejectsSymlinkToDirectoryOutput(t *testing.T) {
	for _, fixture := range outputCommandFixtures {
		t.Run(fixture.name, func(t *testing.T) {
			for _, tc := range []struct {
				name          string
				trailingSlash bool
			}{
				{name: "trailing slash", trailingSlash: true},
				{name: "no trailing slash", trailingSlash: false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					directory, err := os.MkdirTemp(".", ".tmp-output-directory-*")
					require.NoError(t, err)
					t.Cleanup(func() {
						require.NoError(t, os.RemoveAll(directory))
					})
					target := filepath.Join(directory, "realdir")
					require.NoError(t, os.Mkdir(target, 0o700))
					link := filepath.Join(directory, "link")
					require.NoError(t, os.Symlink("realdir", link))
					output := link
					if tc.trailingSlash {
						output += string(filepath.Separator)
					}
					args := append(fixture.write(t, directory), "-output", output)

					err = run(args)

					require.ErrorContains(t, err, "is a directory")
					linkInfo, err := os.Lstat(link)
					require.NoError(t, err)
					require.NotZero(t, linkInfo.Mode()&fs.ModeSymlink, "output must still be a symbolic link")
					entries, err := os.ReadDir(target)
					require.NoError(t, err)
					require.Empty(t, entries, "no file must appear at the link's target")
				})
			}
		})
	}
}

// TestRunSchemaWritesThroughOutputSymlink pins what happens when -output
// names a symbolic link. Renaming a temporary file over the link's own path
// would replace the link with a regular file and leave the file it pointed
// at holding stale content, while the run still reported success, so the
// write has to resolve the link first and replace its target instead.
func TestRunSchemaWritesThroughOutputSymlink(t *testing.T) {
	t.Run("link to a non-generated target is rejected", func(t *testing.T) {
		directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, os.RemoveAll(directory))
		})
		input := schemaFixtureInput(t, directory)
		target := filepath.Join(directory, "target.go")
		output := filepath.Join(directory, "output_gen.go")
		sentinel := []byte("SENTINEL-DATA")
		require.NoError(t, os.WriteFile(target, sentinel, 0o600))
		require.NoError(t, os.Symlink("target.go", output))

		err = run([]string{"schema", "-input", input, "-package", "generated", "-output", output})

		require.ErrorContains(t, err, "must end in _gen.go")
		got, err := os.ReadFile(target)
		require.NoError(t, err)
		require.Equal(t, sentinel, got)
	})

	t.Run("link to a file in the same directory keeps the link and updates its target", func(t *testing.T) {
		directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, os.RemoveAll(directory))
		})
		input := schemaFixtureInput(t, directory)
		target := filepath.Join(directory, "target_gen.go")
		output := filepath.Join(directory, "output_gen.go")
		require.NoError(t, os.WriteFile(target, []byte("SENTINEL-DATA"), 0o644))
		// os.WriteFile masks perm with the umask, so set the bits
		// explicitly to keep the fixture deterministic.
		require.NoError(t, os.Chmod(target, 0o644))
		require.NoError(t, os.Symlink("target_gen.go", output))

		require.NoError(t, run([]string{"schema", "-input", input, "-package", "generated", "-output", output}))

		link, err := os.Lstat(output)
		require.NoError(t, err)
		require.NotZero(t, link.Mode()&fs.ModeSymlink, "output must still be a symbolic link")
		destination, err := os.Readlink(output)
		require.NoError(t, err)
		require.Equal(t, "target_gen.go", destination)
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
		require.ElementsMatch(t, []string{"schema.json", "target_gen.go", "output_gen.go"}, names)
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
		target := filepath.Join(targetDirectory, "target_gen.go")
		output := filepath.Join(linkDirectory, "output_gen.go")
		require.NoError(t, os.WriteFile(target, []byte("SENTINEL-DATA"), 0o600))
		require.NoError(t, os.Symlink(filepath.Join("..", "target", "target_gen.go"), output))
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
		target := filepath.Join(directory, "target_gen.go")
		output := filepath.Join(directory, "output_gen.go")
		require.NoError(t, os.Symlink("target_gen.go", output))

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

	// A link is reached through the directories that lead to it, and those
	// can be links themselves. Joining a relative target against the
	// lexically spelled parent collapses "alias/.." even when alias points
	// into another directory, which sends the generated source to a file
	// nothing points at and leaves the real target missing.
	t.Run("link reached through a symlinked parent directory resolves against the directory the link lives in", func(t *testing.T) {
		root, err := os.MkdirTemp(".", ".tmp-schema-command-*")
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, os.RemoveAll(root))
		})
		input := schemaFixtureInput(t, root)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "actual", "nested"), 0o700))
		require.NoError(t, os.Mkdir(filepath.Join(root, "link"), 0o700))
		require.NoError(t, os.Symlink(filepath.Join("..", "actual", "nested"), filepath.Join(root, "link", "alias")))
		require.NoError(t, os.Symlink(filepath.Join("..", "target_gen.go"), filepath.Join(root, "actual", "nested", "output_gen.go")))
		output := filepath.Join(root, "link", "alias", "output_gen.go")
		// This is where the filesystem, and therefore an ordinary open of
		// output, puts the link's target.
		target := filepath.Join(root, "actual", "target_gen.go")

		require.NoError(t, run([]string{"schema", "-input", input, "-package", "generated", "-output", output}))

		source, err := os.ReadFile(target)
		require.NoError(t, err)
		require.Contains(t, string(source), "func Users() UsersTable {")
		require.NoFileExists(t, filepath.Join(root, "link", "target_gen.go"), "the lexical parent must not receive the generated source")
		link, err := os.Lstat(filepath.Join(root, "actual", "nested", "output_gen.go"))
		require.NoError(t, err)
		require.NotZero(t, link.Mode()&fs.ModeSymlink, "output must still be a symbolic link")
	})

	// resolveOutputPath has to draw its line where an ordinary open draws
	// one, and maxOutputSymlinkDepth records which kernel limit it matches.
	// A chain that long resolves through an ordinary open, destination
	// included, so it must resolve here too, and only the link past it is
	// too many. Both cases are expressed in terms of the constant, so they
	// follow it if it ever changes.
	t.Run("a chain of links resolves up to the depth cap", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			links   int
			wantErr bool
		}{
			{name: "a chain as long as the cap reaches its target", links: maxOutputSymlinkDepth},
			{name: "one link past the cap is too many levels", links: maxOutputSymlinkDepth + 1, wantErr: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
				require.NoError(t, err)
				t.Cleanup(func() {
					require.NoError(t, os.RemoveAll(directory))
				})
				input := schemaFixtureInput(t, directory)
				target := filepath.Join(directory, "target_gen.go")
				// Every link but the last points at the next one, and the
				// last points at a path that does not exist yet, so the
				// chain ends the way a checked-in link to a not yet
				// generated file does.
				for i := range tc.links {
					name := filepath.Join(directory, fmt.Sprintf("l%d_gen.go", i))
					next := fmt.Sprintf("l%d_gen.go", i+1)
					if i == tc.links-1 {
						next = "target_gen.go"
					}
					require.NoError(t, os.Symlink(next, name))
				}
				output := filepath.Join(directory, "l0_gen.go")

				err = run([]string{"schema", "-input", input, "-package", "generated", "-output", output})

				if tc.wantErr {
					require.Error(t, err)
					require.Contains(t, err.Error(), "too many levels of symbolic links")
					require.NoFileExists(t, target)
					return
				}
				require.NoError(t, err)
				source, err := os.ReadFile(target)
				require.NoError(t, err)
				require.Contains(t, string(source), "func Users() UsersTable {")
				info, err := os.Stat(target)
				require.NoError(t, err)
				require.Equal(t, fs.FileMode(0o600), info.Mode().Perm())
				link, err := os.Lstat(output)
				require.NoError(t, err)
				require.NotZero(t, link.Mode()&fs.ModeSymlink, "output must still be a symbolic link")
			})
		}
	})

	t.Run("link that points at itself fails without touching anything", func(t *testing.T) {
		directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, os.RemoveAll(directory))
		})
		input := schemaFixtureInput(t, directory)
		output := filepath.Join(directory, "output_gen.go")
		require.NoError(t, os.Symlink("output_gen.go", output))

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
		require.ElementsMatch(t, []string{"schema.json", "output_gen.go"}, names)
	})

	t.Run("two links pointing at each other fail without touching anything", func(t *testing.T) {
		directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, os.RemoveAll(directory))
		})
		input := schemaFixtureInput(t, directory)
		output := filepath.Join(directory, "output_gen.go")
		other := filepath.Join(directory, "other_gen.go")
		require.NoError(t, os.Symlink("other_gen.go", output))
		require.NoError(t, os.Symlink("output_gen.go", other))

		err = run([]string{"schema", "-input", input, "-package", "generated", "-output", output})
		require.Error(t, err)
		require.Contains(t, err.Error(), "too many levels of symbolic links")
	})
}
