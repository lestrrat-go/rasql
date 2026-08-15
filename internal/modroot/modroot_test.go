package modroot_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/modroot"
	"github.com/stretchr/testify/require"
)

// TestFrom pins the walk both callers depend on: generate resolves a
// relative Store.Dir against what it returns, and rasqlgen's init command
// compares -output against -gen-dir on the directory it names. The two
// disagreeing is the bug this package exists to prevent, so what counts as
// a module root is stated here once.
func TestFrom(t *testing.T) {
	t.Run("the nearest go.mod above the starting directory", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/m\n"), 0o600))
		deep := filepath.Join(root, "a", "b", "c")
		require.NoError(t, os.MkdirAll(deep, 0o755))

		require.Equal(t, root, modroot.From(deep))
		require.Equal(t, root, modroot.From(root))
	})

	t.Run("the nearer of two", func(t *testing.T) {
		outer := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(outer, "go.mod"), []byte("module example.com/outer\n"), 0o600))
		inner := filepath.Join(outer, "inner")
		require.NoError(t, os.MkdirAll(inner, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(inner, "go.mod"), []byte("module example.com/inner\n"), 0o600))

		require.Equal(t, inner, modroot.From(inner))
	})

	// A directory named go.mod is not a module file, so it does not stop the
	// walk. Nothing would build there, and stopping would hand a caller a
	// root the go tool does not recognize.
	t.Run("a go.mod that is a directory does not stop the walk", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/m\n"), 0o600))
		decoy := filepath.Join(root, "decoy")
		require.NoError(t, os.MkdirAll(filepath.Join(decoy, "go.mod"), 0o755))

		require.Equal(t, root, modroot.From(decoy))
	})

	// A directory holding no go.mod is never its own module root. What the
	// walk finds above it belongs to whatever the temporary directory sits
	// under, which a test cannot pin, so only the directory itself is
	// asserted about. Reaching the top without finding one at all is
	// reported as an empty string rather than an error: whether a caller
	// needs a base to resolve against is the caller's question, not this
	// walk's.
	t.Run("a directory holding no go.mod is not a module root", func(t *testing.T) {
		dir := t.TempDir()
		require.NotEqual(t, dir, modroot.From(dir))
	})
}
