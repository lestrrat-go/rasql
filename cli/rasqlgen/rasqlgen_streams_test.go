package rasqlgen_test

import (
	"bytes"
	"flag"
	"testing"

	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	"github.com/stretchr/testify/require"
)

// TestRunSendsHelpToOutput holds Run to the split of streams its doc comment
// promises an exported-API caller: help text is command output. Both help
// paths are checked, because the command prints its own top-level usage while
// the flag package prints a subcommand's flag listing, and only the second
// one used to land on the diagnostic writer.
func TestRunSendsHelpToOutput(t *testing.T) {
	testCases := []struct {
		name     string
		args     []string
		expected string
	}{
		{
			name:     "top level",
			args:     []string{"-h"},
			expected: "Usage: rasql codegen <command> [flags]",
		},
		{
			name:     "subcommand",
			args:     []string{"init", "-h"},
			expected: "Usage of rasql codegen init:",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var output, diagnostics bytes.Buffer
			err := rasqlgen.Run(testCase.args, &output, &diagnostics)
			require.ErrorIs(t, err, flag.ErrHelp)
			require.Contains(t, output.String(), testCase.expected)
			require.Empty(t, diagnostics.String())
		})
	}
}

// TestRunSendsParseFailureToDiagnostics is the other half of the same
// contract: what the flag package prints while refusing an argument stays off
// the output stream, so routing help to output must not carry a parse
// diagnostic with it.
func TestRunSendsParseFailureToDiagnostics(t *testing.T) {
	var output, diagnostics bytes.Buffer
	err := rasqlgen.Run([]string{"init", "-unknown"}, &output, &diagnostics)
	require.Error(t, err)
	require.NotErrorIs(t, err, flag.ErrHelp)
	require.Contains(t, diagnostics.String(), "flag provided but not defined: -unknown")
	require.Contains(t, diagnostics.String(), "Usage of rasql codegen init:")
	require.Empty(t, output.String())
}

// TestRunLegacyKeepsHelpOnItsOneWriter pins that routing by the returned
// error is confined to Run: the standalone binary hands one writer to both
// streams and must keep printing everything there.
func TestRunLegacyKeepsHelpOnItsOneWriter(t *testing.T) {
	var writer bytes.Buffer
	require.ErrorIs(t, rasqlgen.RunLegacy([]string{"init", "-h"}, &writer), flag.ErrHelp)
	require.Contains(t, writer.String(), "Usage of init:")
}
