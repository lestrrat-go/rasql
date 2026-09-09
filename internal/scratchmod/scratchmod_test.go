package scratchmod_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/scratchmod"
	"github.com/stretchr/testify/require"
)

// writeRepoGoMod writes a minimal go.mod (and, unless goSum is empty, a
// go.sum) under a fresh temp directory standing in for the repository
// root, using moduleLine verbatim as the module declaration line so a test
// can vary its spacing and comments.
func writeRepoGoMod(t *testing.T, moduleLine, goSum string) string {
	t.Helper()
	repoRoot := t.TempDir()
	content := moduleLine + "\ngo 1.26\n"
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "go.mod"), []byte(content), 0o600))
	if goSum != "" {
		require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "go.sum"), []byte(goSum), 0o600))
	}
	return repoRoot
}

func TestForModuleRenamesAndPointsBack(t *testing.T) {
	repoRoot := writeRepoGoMod(t, "module github.com/lestrrat-go/rasql", "")
	file, err := scratchmod.ForModule(repoRoot, "example.test/fixture")
	require.NoError(t, err)
	require.Equal(t, "example.test/fixture", file.Module.Mod.Path)
	require.Len(t, file.Require, 1)
	require.Equal(t, "github.com/lestrrat-go/rasql", file.Require[0].Mod.Path)
	require.Equal(t, "v0.0.0", file.Require[0].Mod.Version)
	require.Len(t, file.Replace, 1)
	require.Equal(t, "github.com/lestrrat-go/rasql", file.Replace[0].Old.Path)
	require.Equal(t, filepath.ToSlash(repoRoot), file.Replace[0].New.Path)
}

// TestForModuleSurvivesModuleLineVariations covers the case that used to
// defeat a plain strings.Replace against the module line: a trailing
// comment or different spacing that a fixed pattern would no longer match.
// ForModule parses the file structurally instead of matching text, so it
// renames the module correctly regardless.
func TestForModuleSurvivesModuleLineVariations(t *testing.T) {
	variations := []string{
		"module   github.com/lestrrat-go/rasql",
		"module github.com/lestrrat-go/rasql // the module",
	}
	for _, moduleLine := range variations {
		t.Run(moduleLine, func(t *testing.T) {
			repoRoot := writeRepoGoMod(t, moduleLine, "")
			file, err := scratchmod.ForModule(repoRoot, "example.test/fixture")
			require.NoError(t, err)
			require.Equal(t, "example.test/fixture", file.Module.Mod.Path)
			require.Len(t, file.Replace, 1)
			require.Equal(t, "github.com/lestrrat-go/rasql", file.Replace[0].Old.Path)
		})
	}
}

func TestForModuleReturnsErrorForUnparsableGoMod(t *testing.T) {
	repoRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "go.mod"), []byte("not a go.mod\x00file"), 0o600))
	_, err := scratchmod.ForModule(repoRoot, "example.test/fixture")
	require.Error(t, err)
}

func TestForModuleReturnsErrorWhenGoModMissing(t *testing.T) {
	_, err := scratchmod.ForModule(t.TempDir(), "example.test/fixture")
	require.Error(t, err)
}

func TestWriteWritesGoModAndGoSum(t *testing.T) {
	repoRoot := writeRepoGoMod(t, "module github.com/lestrrat-go/rasql", "example.test/dep v1.0.0 h1:abc=\n")
	dir := t.TempDir()
	require.NoError(t, scratchmod.Write(dir, repoRoot, "example.test/fixture"))

	goMod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	require.NoError(t, err)
	require.Contains(t, string(goMod), "module example.test/fixture\n")
	require.Contains(t, string(goMod), "require github.com/lestrrat-go/rasql v0.0.0\n")
	require.Contains(t, string(goMod), "replace github.com/lestrrat-go/rasql => "+filepath.ToSlash(repoRoot)+"\n")

	goSum, err := os.ReadFile(filepath.Join(dir, "go.sum"))
	require.NoError(t, err)
	require.Equal(t, "example.test/dep v1.0.0 h1:abc=\n", string(goSum))
}

func TestRepointUpdatesOnlyTheReplaceDirective(t *testing.T) {
	dir := t.TempDir()
	goModPath := filepath.Join(dir, "go.mod")
	original := "module example.test/checkedin\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => ../..\n"
	require.NoError(t, os.WriteFile(goModPath, []byte(original), 0o644))

	require.NoError(t, scratchmod.Repoint(goModPath, "github.com/lestrrat-go/rasql", "/repo/root"))

	updated, err := os.ReadFile(goModPath)
	require.NoError(t, err)
	require.Contains(t, string(updated), "module example.test/checkedin\n")
	require.Contains(t, string(updated), "require github.com/lestrrat-go/rasql v0.0.0\n")
	require.Contains(t, string(updated), "replace github.com/lestrrat-go/rasql => /repo/root\n")
	require.NotContains(t, string(updated), "=> ../..")

	info, err := os.Stat(goModPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode())
}
