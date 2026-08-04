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
// and assert Unix permission bits. Neither the resource-limit API nor the
// signal exists outside Unix, so this file is guarded by //go:build unix.
// A runtime GOOS check cannot stand in for the guard, because the test
// package fails to compile for a non-Unix target before any test runs.

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
