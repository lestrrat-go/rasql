package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// generateScript runs scripts/generate.sh with the given arguments and
// returns its combined output. The script owns the migration step, so the
// test drives the same path the documentation tells a reader to run.
func generateScript(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	command := exec.Command(filepath.Join("..", "scripts", "generate.sh"), args...)
	output, err := command.CombinedOutput()
	return output, err
}

func TestGenerateScriptReportsCurrentStore(t *testing.T) {
	output, err := generateScript(t, "-check")
	require.NoError(t, err, "generate.sh -check: %s", output)
}

func TestGenerateScriptReportsStaleStore(t *testing.T) {
	path := filepath.Join("..", storeDirectory, "schema_gen.go")
	original, err := os.ReadFile(path)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.WriteFile(path, original, 0o644))
	})
	require.NoError(t, os.WriteFile(path, append(original, []byte("\n// stale\n")...), 0o644))

	output, err := generateScript(t, "-check")
	require.Error(t, err, "generate.sh -check should fail on a stale store: %s", output)
}
