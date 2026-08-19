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
	for _, testCase := range []struct {
		name     string
		args     []string
		succeeds bool
		expected string
	}{
		{name: "codegen refused flag", args: []string{"codegen", "init", "-unknown"}, expected: "flag provided but not defined: -unknown"},
		{name: "migrate refused flag", args: []string{"migrate", "plan", "-unknown"}, expected: "flag provided but not defined: -unknown"},
		{name: "codegen help", args: []string{"codegen", "init", "-h"}, succeeds: true, expected: "Usage of rasql codegen init:"},
		{name: "migrate help", args: []string{"migrate", "plan", "-h"}, succeeds: true, expected: "Usage of plan:"},
		{name: "command help", args: []string{"-h"}, succeeds: true, expected: "Usage: rasql <context> <command> [flags]"},
		// A "-h" a flag value consumed is not a help request, so the failure
		// that follows it stays on standard error like any other failure.
		{name: "codegen help token as flag value", args: []string{"codegen", "init", "-dialect", "-h", "-unknown"}, expected: "flag provided but not defined: -unknown"},
		{name: "migrate help token as flag value", args: []string{"migrate", "plan", "-dir", "-h", "-unknown"}, expected: "flag provided but not defined: -unknown"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repository, err := filepath.Abs(filepath.Join("..", ".."))
			require.NoError(t, err)

			var stdout, stderr bytes.Buffer
			command := exec.CommandContext(t.Context(), "go", append([]string{"run", "./cmd/rasql"}, testCase.args...)...)
			command.Dir = repository
			command.Stdout = &stdout
			command.Stderr = &stderr
			err = command.Run()

			if !testCase.succeeds {
				require.Error(t, err, "stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
				require.Empty(t, stdout.String())
				require.Contains(t, stderr.String(), testCase.expected)
				return
			}
			require.NoError(t, err, "stderr:\n%s", stderr.String())
			require.Contains(t, stdout.String(), testCase.expected)
			require.Empty(t, stderr.String())
		})
	}
}
