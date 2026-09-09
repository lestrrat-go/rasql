package conformance

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnresolvedGeneratedQuery(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fixture")
	require.NoError(t, copyTree(filepath.Join("testdata", "unresolved-query"), root))
	lockPath := filepath.Join(root, "rasql.lock.json")
	before, err := os.ReadFile(lockPath)
	require.NoError(t, err)
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	binary := filepath.Join(t.TempDir(), "rasql")
	build := exec.Command("go", "build", "-o", binary, filepath.Join(repoRoot, "cmd/rasql"))
	build.Dir = repoRoot
	// GOCACHE is deliberately shared, not rooted under t.TempDir(): see
	// sharedOfflineGOCACHE's comment in generation_test.go.
	build.Env = offlineBuildEnv(sharedOfflineGOCACHE)
	buildOutput, buildErr := build.CombinedOutput()
	require.NoError(t, buildErr, string(buildOutput))
	command := exec.Command(binary, "schema", "update", "-config", filepath.Join(root, "rasql.json"), "-dsn", "")
	command.Dir = repoRoot
	command.Env = offlineBuildEnv(sharedOfflineGOCACHE)
	output, err := command.CombinedOutput()
	require.Error(t, err)
	var exitError *exec.ExitError
	require.ErrorAs(t, err, &exitError)
	require.Equal(t, 2, exitError.ExitCode())
	require.Equal(t, "schema update: query \"unresolved_result\" value \"opaque\" requires a scalar declaration\n", string(output))
	require.Equal(t, string(before), string(mustReadFile(t, lockPath)))
	_, statErr := os.Stat(filepath.Join(root, "internal", "store"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
