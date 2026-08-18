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
// Each case below spells one directory two ways through a symbolic link, so
// the two names differ everywhere except in what they reach. Most of them
// link to a directory the filesystem already holds; the two dangling cases
// link to one the run itself is about to create.
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
		// -output is a link whose target does not exist yet. Every case
		// above links to something already there; this one does not, and
		// resolving the longest existing prefix is not enough for it,
		// because EvalSymlinks reports the dangling link as ErrNotExist just
		// as it reports a missing name, so the walk drops to the module root
		// and rejoins "target" without ever reading the link. The run that
		// followed accepted the flags and wrote gen/main.go holding `Dir:
		// "target"`, and os.MkdirAll then created the very gen the link
		// points at, so the two flags did name one directory after all. The
		// store never landed there -- generate.Store.Plan refused it later
		// -- but refusing it here is the whole point of this check.
		"output is a dangling link to the scaffold directory": {
			build: func(t *testing.T, root string) {
				require.NoError(t, os.Symlink("gen", filepath.Join(root, "target")))
			},
			output: "target",
			genDir: "gen",
		},
		// The link on -gen-dir dangles instead, and its target is where
		// -output points. The scaffold directory is created through the link
		// by os.MkdirAll, so this too ends with one directory named twice.
		"the dangling scaffold link points at output": {
			build: func(t *testing.T, root string) {
				require.NoError(t, os.Symlink("store", filepath.Join(root, "gen")))
			},
			output: "store",
			genDir: "gen",
		},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			root := initModuleDir(t)
			testCase.build(t, root)

			var out bytes.Buffer
			err := RunLegacy([]string{"init", "-dialect", "sqlite", "-package", "store", "-output", testCase.output, "-gen-dir", testCase.genDir}, &out)
			require.ErrorContains(t, err, "name the same directory")
			require.ErrorContains(t, err, testCase.output)
			require.Empty(t, out.String())

			_, statErr := os.Stat(filepath.Join(testCase.genDir, "main.go"))
			require.ErrorIs(t, statErr, os.ErrNotExist, "the refused run must write no scaffold")
		})
	}
}

// TestInitRejectsGenDirSymlinkOutsideModule requires that containment follows
// a symbolic link before init writes through it. A lexical absolute-path
// check accepts this setup and leaves the scaffold outside the module, where
// go generate ./... cannot reach it.
func TestInitRejectsGenDirSymlinkOutsideModule(t *testing.T) {
	root := initModuleDir(t)
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "gen")))

	var out bytes.Buffer
	err := RunLegacy([]string{"init", "-dialect", "sqlite", "-package", "store", "-output", "internal/store", "-gen-dir", "gen"}, &out)
	require.ErrorContains(t, err, "outside the module")
	require.Empty(t, out.String())

	_, statErr := os.Stat(filepath.Join(outside, "main.go"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

// TestInitAcceptsDistinctDirectoriesReachedThroughALink is the control for
// the test above: a symbolic link on the path is not itself a reason to
// refuse, and two names that resolve to two directories still scaffold.
func TestInitAcceptsDistinctDirectoriesReachedThroughALink(t *testing.T) {
	root := initModuleDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "real", "gen"), 0o755))
	require.NoError(t, os.Symlink("real", filepath.Join(root, "link")))

	var out bytes.Buffer
	require.NoError(t, RunLegacy([]string{"init", "-dialect", "sqlite", "-package", "store", "-output", "link/store", "-gen-dir", "real/gen"}, &out))

	source, err := os.ReadFile(filepath.Join(root, "real", "gen", "main.go"))
	require.NoError(t, err)
	require.Contains(t, string(source), `Dir:     "link/store"`)
}

// TestInitAcceptsADanglingLinkToAnotherDirectory is the second control: a
// link whose target does not exist yet is now read by hand, so what it names
// has to matter. This one names a directory -gen-dir does not, and the run
// scaffolds as any other pair of distinct directories does.
func TestInitAcceptsADanglingLinkToAnotherDirectory(t *testing.T) {
	root := initModuleDir(t)
	require.NoError(t, os.Symlink("store", filepath.Join(root, "target")))

	var out bytes.Buffer
	require.NoError(t, RunLegacy([]string{"init", "-dialect", "sqlite", "-package", "store", "-output", "target", "-gen-dir", "gen"}, &out))

	source, err := os.ReadFile(filepath.Join(root, "gen", "main.go"))
	require.NoError(t, err)
	require.Contains(t, string(source), `Dir:     "target"`)
}

// TestInitTerminatesOnALinkCycle requires that reading links by hand cannot
// loop. Two links pointing at each other resolve to nothing at all:
// filepath.EvalSymlinks refuses the cycle, and following it by hand would
// walk it forever without the hop bound canonicalDirectory carries. The run
// falls back to comparing the two paths as written, which here is the right
// answer anyway, and above all it finishes.
func TestInitTerminatesOnALinkCycle(t *testing.T) {
	root := initModuleDir(t)
	require.NoError(t, os.Symlink("second", filepath.Join(root, "first")))
	require.NoError(t, os.Symlink("first", filepath.Join(root, "second")))

	var out bytes.Buffer
	require.NoError(t, RunLegacy([]string{"init", "-dialect", "sqlite", "-package", "store", "-output", "first", "-gen-dir", "gen"}, &out))

	source, err := os.ReadFile(filepath.Join(root, "gen", "main.go"))
	require.NoError(t, err)
	require.Contains(t, string(source), `Dir:     "first"`)
}
