package rasql

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
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
	require.Contains(t, output.String(), "init      Scaffold the generator program, gen/main.go")
}

func TestRunCodegenRejectsRemovedCommands(t *testing.T) {
	for _, command := range []string{"bootstrap", "schema", "query", "bindings"} {
		t.Run(command, func(t *testing.T) {
			err := Run([]string{"codegen", command}, bytes.NewBuffer(nil), bytes.NewBuffer(nil))
			require.EqualError(t, err, "unknown rasql codegen command \""+command+"\"; expected generate or init")
		})
	}
}

func TestRunMigrateRejectsNoCommand(t *testing.T) {
	err := Run([]string{"migrate"}, &bytes.Buffer{}, &bytes.Buffer{})
	require.EqualError(t, err, "usage: rasql migrate <new|diff|diff-live|plan|apply|status|verify> [flags]")
}

// TestRunKeepsFlagDiagnosticsOffOutput requires that a refused flag is
// reported as a diagnostic and never mixed into command output. A command
// whose output is piped somewhere -- a plan into a file, a status into a
// pipeline -- must not have a diagnostic written into that stream, and the
// error returned here is what the caller reports on the diagnostic stream.
func TestRunKeepsFlagDiagnosticsOffOutput(t *testing.T) {
	for _, args := range [][]string{
		{"codegen", "init", "-unknown"},
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
		{name: "codegen", args: []string{"codegen", "init", "-h"}, expected: "Usage of rasql codegen init:"},
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

// TestRunCodegenInitNamesTheUnifiedCommand requires that init reports the
// command the user actually ran. Every message here names a command to run
// again, so naming the standalone rasqlgen binary would send a user of the
// unified command to a binary they may not have installed.
func TestRunCodegenInitNamesTheUnifiedCommand(t *testing.T) {
	t.Chdir(t.TempDir())
	path := filepath.Join("gen", "main.go")
	initArgs := []string{"codegen", "init", "-dialect", "sqlite", "-package", "store", "-output", "internal/store"}

	var first bytes.Buffer
	require.NoError(t, Run(initArgs, &first, &bytes.Buffer{}))

	var second bytes.Buffer
	err := Run(initArgs, &second, &bytes.Buffer{})
	require.EqualError(t, err, "rasql codegen init: "+path+" already exists; edit it, or pass -force to overwrite it")

	var third bytes.Buffer
	require.NoError(t, Run(append(initArgs, "-force"), &third, &bytes.Buffer{}))
	require.Contains(t, third.String(), "rasql codegen init: -force overwrote "+path)

	// The scaffold's own doc comment tells its reader which command wrote
	// the file and which command rewrites it, so it names the command that
	// ran rather than the standalone binary.
	source, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Contains(t, string(source), "// rasql codegen init wrote this file once.")
	require.NotContains(t, string(source), "rasqlgen init")
}

// TestRunKeepsSwallowedHelpTokenOffOutput requires that a "-h" a flag value
// consumed is not treated as a help request. The run below fails on the flag
// that follows, and that failure -- the flag package's message and the usage
// block under it -- is a diagnostic, so it must stay off the output stream
// exactly as it does when no "-h" appears at all.
func TestRunKeepsSwallowedHelpTokenOffOutput(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		args  []string
		usage string
	}{
		{name: "codegen", args: []string{"codegen", "init", "-dialect", "-h", "-unknown"}, usage: "Usage of rasql codegen init:"},
		{name: "migrate", args: []string{"migrate", "new", "-dir", "-h", "-unknown"}, usage: "Usage of new:"},
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
	err := Run([]string{"codegen", "init", "-dialect", "-h", "-h"}, &output, &diagnostics)
	require.ErrorIs(t, err, flag.ErrHelp)
	require.Contains(t, output.String(), "Usage of rasql codegen init:")
	require.Empty(t, diagnostics.String())
}
