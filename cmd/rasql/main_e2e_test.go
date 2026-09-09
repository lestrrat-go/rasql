package main_test

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGoRunSeparatesDiagnosticsFromOutput drives the binary itself, because
// the two streams only exist there: the packages below it take writers, and
// only main decides that standard output carries command output while
// standard error carries diagnostics. A refused flag must leave standard
// output untouched, and a help request must print there.
func TestGoRunSeparatesDiagnosticsFromOutput(t *testing.T) {
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	binary := filepath.Join(t.TempDir(), "rasql")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/rasql")
	build.Dir = repository
	require.NoError(t, build.Run())
	for _, testCase := range []struct {
		name     string
		args     []string
		succeeds bool
		expected string
	}{
		{name: "codegen refused flag", args: []string{"codegen", "generate", "-unknown"}, expected: "flag provided but not defined: -unknown"},
		{name: "codegen rejects positional dsn", args: []string{"codegen", "generate", "stray", "-dsn", "postgres://tester:auditSyntheticPassword572@localhost/test"}, expected: "unexpected 3 positional arguments; generate accepts flags only"},
		{name: "migrate refused flag", args: []string{"migrate", "plan", "-unknown"}, expected: "flag provided but not defined: -unknown"},
		{name: "codegen help", args: []string{"codegen", "generate", "-h"}, succeeds: true, expected: "Usage of rasql codegen generate:"},
		{name: "migrate help", args: []string{"migrate", "plan", "-h"}, succeeds: true, expected: "Usage of plan:"},
		{name: "command help", args: []string{"-h"}, succeeds: true, expected: "Usage: rasql <context> <command> [flags]"},
		// A "-h" a flag value consumed is not a help request, so the failure
		// that follows it stays on standard error like any other failure.
		{name: "codegen help token as flag value", args: []string{"codegen", "generate", "-config", "-h", "-unknown"}, expected: "flag provided but not defined: -unknown"},
		{name: "migrate help token as flag value", args: []string{"migrate", "plan", "-dir", "-h", "-unknown"}, expected: "flag provided but not defined: -unknown"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			command := exec.CommandContext(t.Context(), binary, testCase.args...)
			command.Dir = repository
			command.Stdout = &stdout
			command.Stderr = &stderr
			err = command.Run()
			if testCase.succeeds {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}

			if !testCase.succeeds {
				require.Equal(t, 2, command.ProcessState.ExitCode(), "stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
				require.Empty(t, stdout.String())
				require.Contains(t, stderr.String(), testCase.expected)
				require.NotContains(t, stdout.String(), "auditSyntheticPassword572")
				require.NotContains(t, stderr.String(), "auditSyntheticPassword572")
				require.NotContains(t, stdout.String(), "postgres://tester:auditSyntheticPassword572@localhost/test")
				require.NotContains(t, stderr.String(), "postgres://tester:auditSyntheticPassword572@localhost/test")
				return
			}
			require.NoError(t, err, "stderr:\n%s", stderr.String())
			require.Contains(t, stdout.String(), testCase.expected)
			require.Empty(t, stderr.String())
		})
	}
}
