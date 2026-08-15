//go:build unix

package rasqlgen

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// This test uses Unix-only symbolic links.

// TestInitRefusesOutputThatResolvesToTheScaffoldDirectory requires that
// -output and -gen-dir are compared as directories, not as path strings.
// Each case below spells one directory two ways through a symbolic link the
// filesystem already holds, so the two names differ everywhere except in
// what they reach.
//
// filepath.Abs, which this check once relied on alone, cannot see any of
// them: it is Clean plus a join with the working directory and resolves no
// link, so "alias" and "gen" stayed two directories to it. init then wrote
// the scaffold, `go generate` filled the one real directory with the store,
// and `go build` reported "found packages main (main.go) and store
// (schema_gen.go)" from then on.
func TestInitRefusesOutputThatResolvesToTheScaffoldDirectory(t *testing.T) {
	testCases := map[string]struct {
		// build prepares the directories and links inside the module root,
		// which is also the working directory.
		build  func(t *testing.T, root string)
		output string
		genDir string
	}{
		// -output names the scaffold directory through a link to it.
		"output is a link to the scaffold directory": {
			build: func(t *testing.T, root string) {
				require.NoError(t, os.Mkdir(filepath.Join(root, "gen"), 0o755))
				require.NoError(t, os.Symlink("gen", filepath.Join(root, "alias")))
			},
			output: "alias",
			genDir: "gen",
		},
		// The link is on the other flag: -gen-dir reaches the store's own
		// directory through it.
		"the scaffold directory is a link to output": {
			build: func(t *testing.T, root string) {
				require.NoError(t, os.Mkdir(filepath.Join(root, "store"), 0o755))
				require.NoError(t, os.Symlink("store", filepath.Join(root, "gen")))
			},
			output: "store",
			genDir: "gen",
		},
		// Neither last element is a link; a parent of one of them is. This
		// is why the check resolves the whole existing prefix rather than
		// only the directory each flag names.
		"a parent of output is a link": {
			build: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, "real", "gen"), 0o755))
				require.NoError(t, os.Symlink("real", filepath.Join(root, "link")))
			},
			output: "link/gen",
			genDir: "real/gen",
		},
		// The store's directory does not exist yet, which is the ordinary
		// case: filepath.EvalSymlinks refuses a path whose last element is
		// missing, so only resolving the longest existing prefix and
		// rejoining the rest catches this one.
		"output does not exist yet under a linked parent": {
			build: func(t *testing.T, root string) {
				require.NoError(t, os.Mkdir(filepath.Join(root, "real"), 0o755))
				require.NoError(t, os.Symlink("real", filepath.Join(root, "link")))
			},
			output: "link/gen",
			genDir: "real/gen",
		},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			root := initModuleDir(t)
			testCase.build(t, root)

			var out bytes.Buffer
			err := Run([]string{"init", "-dialect", "sqlite", "-package", "store", "-output", testCase.output, "-gen-dir", testCase.genDir}, &out)
			require.ErrorContains(t, err, "name the same directory")
			require.ErrorContains(t, err, testCase.output)
			require.Empty(t, out.String())

			_, statErr := os.Stat(filepath.Join(testCase.genDir, "main.go"))
			require.ErrorIs(t, statErr, os.ErrNotExist, "the refused run must write no scaffold")
		})
	}
}

// TestInitAcceptsDistinctDirectoriesReachedThroughALink is the control for
// the test above: a symbolic link on the path is not itself a reason to
// refuse, and two names that resolve to two directories still scaffold.
func TestInitAcceptsDistinctDirectoriesReachedThroughALink(t *testing.T) {
	root := initModuleDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "real", "gen"), 0o755))
	require.NoError(t, os.Symlink("real", filepath.Join(root, "link")))

	var out bytes.Buffer
	require.NoError(t, Run([]string{"init", "-dialect", "sqlite", "-package", "store", "-output", "link/store", "-gen-dir", "real/gen"}, &out))

	source, err := os.ReadFile(filepath.Join(root, "real", "gen", "main.go"))
	require.NoError(t, err)
	require.Contains(t, string(source), `Dir:     "link/store"`)
}
