package rasql

import (
	"bytes"
	"flag"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunHelpDescribesContexts(t *testing.T) {
	var output bytes.Buffer
	err := Run([]string{"-h"}, &output, &bytes.Buffer{})
	require.ErrorIs(t, err, flag.ErrHelp)
	require.Contains(t, output.String(), "codegen")
	require.Contains(t, output.String(), "migrate")
}

func TestRunRejectsNoArguments(t *testing.T) {
	err := Run(nil, bytes.NewBuffer(nil), bytes.NewBuffer(nil))
	require.EqualError(t, err, "usage: rasql <codegen|migrate> <command> [flags]")
}

func TestRunRejectsUnknownContext(t *testing.T) {
	err := Run([]string{"unknown"}, &bytes.Buffer{}, &bytes.Buffer{})
	require.EqualError(t, err, "unknown rasql command \"unknown\"; expected codegen or migrate")
}

func TestRunCodegenHelp(t *testing.T) {
	var output bytes.Buffer
	err := Run([]string{"codegen", "-h"}, &output, &bytes.Buffer{})
	require.ErrorIs(t, err, flag.ErrHelp)
	require.Contains(t, output.String(), "Usage: rasql codegen <command> [flags]")
	require.Contains(t, output.String(), "generate  Generate the store package from a live database")
}

func TestRunCodegenRejectsRemovedCommands(t *testing.T) {
	for _, command := range []string{"bootstrap", "schema", "query", "bindings"} {
		t.Run(command, func(t *testing.T) {
			err := Run([]string{"codegen", command}, bytes.NewBuffer(nil), bytes.NewBuffer(nil))
			require.EqualError(t, err, "unknown rasql codegen command \""+command+"\"; expected generate")
		})
	}
}

func TestRunMigrateRejectsNoCommand(t *testing.T) {
	err := Run([]string{"migrate"}, &bytes.Buffer{}, &bytes.Buffer{})
	require.EqualError(t, err, "usage: rasql migrate <diff|diff-live|dump|plan|apply|revert|status|verify> [flags]")
}

// TestRunKeepsFlagDiagnosticsOffOutput requires that a refused flag is
// reported as a diagnostic and never mixed into command output. A command
// whose output is piped somewhere -- a plan into a file, a status into a
// pipeline -- must not have a diagnostic written into that stream, and the
// error returned here is what the caller reports on the diagnostic stream.
func TestRunKeepsFlagDiagnosticsOffOutput(t *testing.T) {
	for _, args := range [][]string{
		{"codegen", "generate", "-unknown"},
		{"migrate", "plan", "-unknown"},
	} {
		t.Run(args[0], func(t *testing.T) {
			t.Chdir(t.TempDir())

			var output, diagnostics bytes.Buffer
			err := Run(args, &output, &diagnostics)
			require.Error(t, err)
			require.Empty(t, output.String())
			require.Contains(t, diagnostics.String(), "flag provided but not defined: -unknown")
		})
	}
}

// TestRunPrintsCommandFlagsOnOutput requires the opposite of the case
// above: a command asked for its flags prints them as output, because that
// listing is what the caller ran the command for.
func TestRunPrintsCommandFlagsOnOutput(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		args     []string
		expected string
	}{
		{name: "codegen", args: []string{"codegen", "generate", "-h"}, expected: "Usage of rasql codegen generate:"},
		{name: "migrate", args: []string{"migrate", "plan", "-h"}, expected: "Usage of plan:"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Chdir(t.TempDir())

			var output, diagnostics bytes.Buffer
			err := Run(testCase.args, &output, &diagnostics)
			require.ErrorIs(t, err, flag.ErrHelp)
			require.Contains(t, output.String(), testCase.expected)
			require.Empty(t, diagnostics.String())
		})
	}
}

func TestRunKeepsSwallowedHelpTokenOffOutput(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		args  []string
		usage string
	}{
		{name: "codegen", args: []string{"codegen", "generate", "-dialect", "-h", "-unknown"}, usage: "Usage of rasql codegen generate:"},
		{name: "migrate", args: []string{"migrate", "plan", "-dir", "-h", "-unknown"}, usage: "Usage of plan:"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Chdir(t.TempDir())

			var output, diagnostics bytes.Buffer
			err := Run(testCase.args, &output, &diagnostics)
			require.Error(t, err)
			require.NotErrorIs(t, err, flag.ErrHelp)
			require.Empty(t, output.String())
			require.Contains(t, diagnostics.String(), "flag provided but not defined: -unknown")
			require.Contains(t, diagnostics.String(), testCase.usage)
		})
	}
}

// TestRunKeepsHelpAfterSwallowedTokenOnOutput requires that a genuine help
// request still prints as command output when an earlier flag value happened
// to hold a help token. The value below is a literal "-h" the dialect flag
// consumed, and the "-h" after it is the request itself.
func TestRunKeepsHelpAfterSwallowedTokenOnOutput(t *testing.T) {
	t.Chdir(t.TempDir())

	var output, diagnostics bytes.Buffer
	err := Run([]string{"codegen", "generate", "-dialect", "-h", "-h"}, &output, &diagnostics)
	require.ErrorIs(t, err, flag.ErrHelp)
	require.Contains(t, output.String(), "Usage of rasql codegen generate:")
	require.Empty(t, diagnostics.String())
}
