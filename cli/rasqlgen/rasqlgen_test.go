package rasqlgen

import (
	"bytes"
	"flag"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunRejectsNoArguments(t *testing.T) {
	err := Run(nil, bytes.NewBuffer(nil))
	require.EqualError(t, err, "usage: rasqlgen <init> [flags]")
}

func TestRunRejectsRemovedCommands(t *testing.T) {
	for _, command := range []string{"bootstrap", "schema", "query"} {
		t.Run(command, func(t *testing.T) {
			err := Run([]string{command}, bytes.NewBuffer(nil))
			require.EqualError(t, err, "unknown rasqlgen command \""+command+"\"; expected init")
		})
	}
}

func TestRunHelp(t *testing.T) {
	var output bytes.Buffer
	err := Run([]string{"-h"}, &output)
	require.ErrorIs(t, err, flag.ErrHelp)
	require.Contains(t, output.String(), "Usage: rasqlgen <command> [flags]")
	require.Contains(t, output.String(), "init      Scaffold the generator program, gen/main.go")
	require.NotContains(t, output.String(), "bootstrap")
	require.NotContains(t, output.String(), "schema")
	require.NotContains(t, output.String(), "query")
}
