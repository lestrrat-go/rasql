package rasqlgen

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunRejectsNoArguments(t *testing.T) {
	err := RunLegacy(nil, bytes.NewBuffer(nil))
	require.EqualError(t, err, "usage: rasqlgen <generate|init> [flags]")
}

func TestRunRejectsRemovedCommands(t *testing.T) {
	for _, command := range []string{"bootstrap", "schema", "query"} {
		t.Run(command, func(t *testing.T) {
			err := RunLegacy([]string{command}, bytes.NewBuffer(nil))
			require.EqualError(t, err, "unknown rasqlgen command \""+command+"\"; expected generate or init")
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
	require.ErrorIs(t, RunLegacy([]string{"init", "-h"}, &help), flag.ErrHelp)
	require.Contains(t, help.String(), "Usage of init:")

	var refused bytes.Buffer
	require.Error(t, RunLegacy([]string{"init", "-unknown"}, &refused))
	require.Contains(t, refused.String(), "flag provided but not defined: -unknown")
}

// TestRunLegacyNamesTheStandaloneCommand requires that the standalone
// binary keeps naming itself in the messages that tell a user what to run
// next, where the unified command names itself instead.
func TestRunLegacyNamesTheStandaloneCommand(t *testing.T) {
	t.Chdir(t.TempDir())
	path := filepath.Join("gen", "main.go")
	args := []string{"init", "-dialect", "sqlite", "-package", "store", "-output", "internal/store"}

	var first bytes.Buffer
	require.NoError(t, RunLegacy(args, &first))

	source, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(source), "// rasqlgen init wrote this file once.")

	var second bytes.Buffer
	err = RunLegacy(args, &second)
	require.EqualError(t, err, "rasqlgen init: "+path+" already exists; edit it, or pass -force to overwrite it")

	var third bytes.Buffer
	require.NoError(t, RunLegacy(append(args, "-force"), &third))
	require.Contains(t, third.String(), "rasqlgen init: -force overwrote "+path)
}

func TestRunHelp(t *testing.T) {
	var output bytes.Buffer
	err := RunLegacy([]string{"-h"}, &output)
	require.ErrorIs(t, err, flag.ErrHelp)
	require.Contains(t, output.String(), "Usage: rasqlgen <command> [flags]")
	require.Contains(t, output.String(), "generate  Generate the store package from a live database")
	require.Contains(t, output.String(), "init      Scaffold the generator program, gen/main.go")
	require.NotContains(t, output.String(), "bootstrap")
	require.NotContains(t, output.String(), "schema")
	require.NotContains(t, output.String(), "query")
}
