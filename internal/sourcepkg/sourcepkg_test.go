package sourcepkg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAsDirectoryPatternNormalizesRelativePaths(t *testing.T) {
	tests := map[string]string{
		"internal/tables":      "./internal/tables",
		"./internal/tables":    "./internal/tables",
		"../internal/tables":   "../internal/tables",
		".":                    ".",
		"..":                   "..",
		"/abs/internal/tables": "/abs/internal/tables",
	}
	for input, want := range tests {
		require.Equal(t, want, asDirectoryPattern(input), "input %q", input)
	}
}

// This test spawns a real `go list`.
func TestResolveReportsImportPathAndModuleDir(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	resolvedRepoRoot, err := filepath.EvalSymlinks(repoRoot)
	require.NoError(t, err)
	t.Chdir(repoRoot)

	pkg, err := Resolve(t.Context(), "./internal/sourcepkg")
	require.NoError(t, err)
	require.Equal(t, "github.com/lestrrat-go/rasql/internal/sourcepkg", pkg.ImportPath)
	require.Equal(t, resolvedRepoRoot, pkg.ModuleDir)
}

// This test spawns a real `go list`.
func TestResolveRejectsAMissingDirectory(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	t.Chdir(repoRoot)

	_, err = Resolve(t.Context(), "./internal/sourcepkg/nowhere-at-all")
	require.Error(t, err)
	require.ErrorContains(t, err, "directory not found")
}

// newScratchModule builds a minimal Go module in t.TempDir() with no
// dependency on this repository: the programs Capture runs in the tests
// below import nothing but the standard library, so no replace directive
// and no go.sum are needed. The "go 1.26.0" directive matches this
// repository's own go.mod -- a hand-written two-component "go 1.26" makes
// `go run` fail with "go: updates to go.mod needed" on go1.26.1.
func newScratchModule(t *testing.T) string {
	t.Helper()
	moduleDir := t.TempDir()
	goMod := "module example.com/sourcepkg-scratch\n\ngo 1.26.0\n"
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte(goMod), 0o600))
	return moduleDir
}

// requireNoLeftoverTempDir asserts that no directory anywhere under root's
// tree has a name starting with prefix.
func requireNoLeftoverTempDir(t *testing.T, root, prefix string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
			t.Errorf("leftover temporary directory %s", path)
		}
		return nil
	})
	require.NoError(t, err)
}

const captureSuccessProgramSource = `package main

import "fmt"

func main() {
	fmt.Print("hello from capture")
}
`

// This test spawns a real `go run`.
func TestCaptureReturnsProgramStdout(t *testing.T) {
	moduleDir := newScratchModule(t)
	pkg := Package{ModuleDir: moduleDir}

	output, err := pkg.Capture(t.Context(), ".sourcepkg-capture-test-", []byte(captureSuccessProgramSource))
	require.NoError(t, err)
	require.Equal(t, "hello from capture", string(output))

	requireNoLeftoverTempDir(t, moduleDir, ".sourcepkg-capture-test-")
}

const captureFailureProgramSource = `package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "boom from capture")
	os.Exit(1)
}
`

// This test spawns a real `go run`.
func TestCaptureFoldsStderrIntoTheError(t *testing.T) {
	moduleDir := newScratchModule(t)
	pkg := Package{ModuleDir: moduleDir}

	_, err := pkg.Capture(t.Context(), ".sourcepkg-capture-fail-", []byte(captureFailureProgramSource))
	require.Error(t, err)
	require.ErrorContains(t, err, "boom from capture")

	requireNoLeftoverTempDir(t, moduleDir, ".sourcepkg-capture-fail-")
}
