package rasqlgen

import (
	"bytes"
	"flag"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunRejectsNoArguments(t *testing.T) {
	err := RunLegacy(nil, bytes.NewBuffer(nil))
	require.EqualError(t, err, "usage: rasqlgen <generate> [flags]")
}

func TestRunRejectsRemovedCommands(t *testing.T) {
	for _, command := range []string{"bootstrap", "schema", "query", "init"} {
		t.Run(command, func(t *testing.T) {
			err := RunLegacy([]string{command}, bytes.NewBuffer(nil))
			require.EqualError(t, err, "unknown rasqlgen command \""+command+"\"; expected generate")
		})
	}
}

// TestRunLegacyKeepsTheStandaloneOutput pins what the standalone rasqlgen
// binary prints, because adding the unified rasql command must not change
// it: one writer takes command output and flag diagnostics together, and a
// flag set still names the bare subcommand in its usage header.
func TestRunLegacyKeepsTheStandaloneOutput(t *testing.T) {
	t.Chdir(t.TempDir())

	var help bytes.Buffer
	require.ErrorIs(t, RunLegacy([]string{"generate", "-h"}, &help), flag.ErrHelp)
	require.Contains(t, help.String(), "Usage of generate:")

	var refused bytes.Buffer
	require.Error(t, RunLegacy([]string{"generate", "-unknown"}, &refused))
	require.Contains(t, refused.String(), "flag provided but not defined: -unknown")
}

// TestRunLegacyNamesTheStandaloneCommand requires that the standalone
// binary keeps naming itself where the unified command names itself, which
// is what a usage line and a refusal message both do.
func TestRunLegacyNamesTheStandaloneCommand(t *testing.T) {
	t.Chdir(t.TempDir())

	err := RunLegacy(nil, bytes.NewBuffer(nil))
	require.EqualError(t, err, "usage: rasqlgen <generate> [flags]")

	var usage bytes.Buffer
	require.ErrorIs(t, RunLegacy([]string{"-h"}, &usage), flag.ErrHelp)
	require.Contains(t, usage.String(), "Usage: rasqlgen <command> [flags]")
}
func TestRunHelp(t *testing.T) {
	var output bytes.Buffer
	err := RunLegacy([]string{"-h"}, &output)
	require.ErrorIs(t, err, flag.ErrHelp)
	require.Contains(t, output.String(), "Usage: rasqlgen <command> [flags]")
	require.Contains(t, output.String(), "generate  Generate the store package from a live database")
	require.Contains(t, output.String(), "Settings live in rasql.json at the module root")
	require.NotContains(t, output.String(), "bootstrap")
	require.NotContains(t, output.String(), "init")
	require.NotContains(t, output.String(), "schema")
	require.NotContains(t, output.String(), "query")
}
