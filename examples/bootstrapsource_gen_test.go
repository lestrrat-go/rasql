package examples_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// bootstrapSourceGenerated lists the files the bootstrapsource example
// generates. Both are checked in for the same reason examples/store is: a
// build compiles them, so output the generator stopped producing cannot
// pass unnoticed.
var bootstrapSourceGenerated = []string{
	"examples/bootstrapsource/internal/tables/users_gen.go",
	"examples/bootstrapsource/internal/tables/tables_gen.go",
	"examples/bootstrapsource/internal/tables/hints.go",
}

// TestBootstrapSourceExampleGenerates runs the example through the very
// directive the documentation shows, `//go:generate go run ./gen` in the
// bootstrapsource package, rather than calling rasqlgen.Run or
// generate.WriteDescriptionPackage itself. Only running it that way
// exercises the working directory the directive gives it, which is what
// decides whether the relative output path in gen/main.go resolves at all,
// and proves the example's own throwaway SQLite database, not a hand-kept
// fixture, is what produced the checked-in files.
func TestBootstrapSourceExampleGenerates(t *testing.T) {
	before := make(map[string][]byte, len(bootstrapSourceGenerated))
	for _, name := range bootstrapSourceGenerated {
		contents, err := os.ReadFile(filepath.Join(repositoryRoot, name))
		require.NoError(t, err)
		before[name] = contents
	}

	// No Dir: `go test` runs this package with its own directory as the
	// working directory, so ./bootstrapsource is the example package.
	command := exec.CommandContext(t.Context(), "go", "generate", "./bootstrapsource")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "go generate ./bootstrapsource: %s", output)

	stale := false
	for _, name := range bootstrapSourceGenerated {
		path := filepath.Join(repositoryRoot, name)
		after, err := os.ReadFile(path)
		require.NoError(t, err)
		if !bytes.Equal(before[name], after) {
			stale = true
		}
	}
	require.Contains(t, string(mustReadFile(t, filepath.Join(repositoryRoot, bootstrapSourceGenerated[0]))), "func UsersDef() schema.TableDef {")

	if *updateDocs {
		return
	}
	if !stale {
		return
	}
	// Put the checked-in files back before failing, so a stale example
	// leaves the tree as it found it.
	for _, name := range bootstrapSourceGenerated {
		require.NoError(t, os.WriteFile(filepath.Join(repositoryRoot, name), before[name], 0o644))
	}
	require.Fail(t, "bootstrapsource example is stale; run `go test ./examples/ -update-docs`")
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	return contents
}
