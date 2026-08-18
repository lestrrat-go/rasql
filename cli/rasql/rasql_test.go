package rasql

import (
	"bytes"
	"flag"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunHelpDescribesContexts(t *testing.T) {
	var output bytes.Buffer
	err := Run([]string{"-h"}, &output)
	require.ErrorIs(t, err, flag.ErrHelp)
	require.Contains(t, output.String(), "codegen")
	require.Contains(t, output.String(), "migrate")
}

func TestRunRejectsNoArguments(t *testing.T) {
	err := Run(nil, bytes.NewBuffer(nil))
	require.EqualError(t, err, "usage: rasql <codegen|migrate> <command> [flags]")
}

func TestRunRejectsUnknownContext(t *testing.T) {
	err := Run([]string{"unknown"}, &bytes.Buffer{})
	require.EqualError(t, err, "unknown rasql command \"unknown\"; expected codegen or migrate")
}

func TestRunCodegenHelp(t *testing.T) {
	var output bytes.Buffer
	err := Run([]string{"codegen", "-h"}, &output)
	require.ErrorIs(t, err, flag.ErrHelp)
	require.Contains(t, output.String(), "Usage: rasql codegen <command> [flags]")
	require.Contains(t, output.String(), "init      Scaffold the generator program, gen/main.go")
}

func TestRunCodegenRejectsRemovedCommands(t *testing.T) {
	for _, command := range []string{"bootstrap", "schema", "query", "bindings"} {
		t.Run(command, func(t *testing.T) {
			err := Run([]string{"codegen", command}, bytes.NewBuffer(nil))
			require.EqualError(t, err, "unknown rasql codegen command \""+command+"\"; expected init")
		})
	}
}

func TestRunMigrateRejectsNoCommand(t *testing.T) {
	err := Run([]string{"migrate"}, &bytes.Buffer{})
	require.EqualError(t, err, "usage: rasql migrate <new|diff|diff-live|plan|apply|status|verify> [flags]")
}
