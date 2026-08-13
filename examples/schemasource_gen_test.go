package examples_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// schemaSourceGenerated is the file the schemasource example generates. It
// is checked in for the same reason examples/store is: a build compiles it,
// so output the generator stopped producing cannot pass unnoticed.
const schemaSourceGenerated = "examples/schemasource/internal/store/users_gen.go"

// TestSchemaSourceExampleGenerates runs the example through the very
// directive the documentation shows, `//go:generate go run ./gen` in the
// schema package, rather than compiling the program or calling
// generate.WritePackage itself. Only running it that way exercises the
// working directory the directive gives it, which is what decides whether
// the relative output path in gen/main.go resolves at all.
func TestSchemaSourceExampleGenerates(t *testing.T) {
	path := filepath.Join(repositoryRoot, schemaSourceGenerated)
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	// No Dir: `go test` runs this package with its own directory as the
	// working directory, so ./schemasource is the example package.
	command := exec.CommandContext(t.Context(), "go", "generate", "./schemasource")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "go generate ./schemasource: %s", output)

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(after), "func Users() UsersTable {")
	if *updateDocs || bytes.Equal(before, after) {
		return
	}
	// Put the checked-in file back before failing, so a stale example
	// leaves the tree as it found it.
	require.NoError(t, os.WriteFile(path, before, 0o644))
	require.Equal(t, string(before), string(after),
		"%s is stale; run `go test ./examples/ -update-docs`", schemaSourceGenerated)
}

// TestSchemaSourceExampleReportsFailureWithNonzeroExit runs the same
// program from a directory that has no internal/store, which is what the
// example itself does when a user copies it without creating the output
// directory first. A generate step that printed the failure and exited 0
// would look successful while producing nothing.
func TestSchemaSourceExampleReportsFailureWithNonzeroExit(t *testing.T) {
	// The premise of the test: this package's own directory, which is the
	// working directory below, holds no internal/store for the example to
	// write into.
	require.NoDirExists(t, filepath.Join("internal", "store"))

	command := exec.CommandContext(t.Context(), "go", "run", "./schemasource/gen")
	output, err := command.CombinedOutput()
	require.Error(t, err, "expected a nonzero exit, got: %s", output)

	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 1, exit.ExitCode(), "output: %s", output)
	require.Contains(t, string(output), "failed to write schema package")
}
