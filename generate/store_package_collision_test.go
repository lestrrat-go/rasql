package generate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestStorePlanRefusesADirectoryHoldingAnotherPackage requires that a Dir
// already holding a Go file of another package is refused before anything
// is written. Go allows one package per directory, so a run that wrote the
// store there leaves a directory the toolchain refuses outright: `go build`
// reports "found packages main (main.go) and store (schema_gen.go)" and
// nothing the store does afterwards repairs it.
//
// This is the last place the collision can be caught. rasqlgen's init
// command refuses the flags that arrange it, but a symbolic link made after
// init runs, and any hand-written Store, reach the directory with nothing
// at init time left to consult.
func TestStorePlanRefusesADirectoryHoldingAnotherPackage(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "gen")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))

	store := generate.Store{Package: "store", Root: root, Dir: "gen", Tables: []schema.TableDef{usersTableDef()}}

	_, err := store.Plan()
	require.Error(t, err)
	require.ErrorContains(t, err, `already holds package "main"`)
	require.ErrorContains(t, err, "main.go")
	require.ErrorContains(t, err, `package "store"`)

	require.ErrorContains(t, store.Write(), `already holds package "main"`)
	require.ErrorContains(t, store.Check(), `already holds package "main"`)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "the refused run must write nothing")
	require.Equal(t, "main.go", entries[0].Name())
}

// TestStorePlanRefusesACaseVariantUserPackage checks that a file whose name
// differs from a planned destination only by case is still treated as a
// separate file on a case-sensitive filesystem. The directory scan must not
// fold that name before asking the filesystem whether both names are one
// entry, or it can let a second package into the generated directory.
func TestStorePlanRefusesACaseVariantUserPackage(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "gen")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	foreign := filepath.Join(dir, "USERS_gen.go")
	require.NoError(t, os.WriteFile(foreign, []byte("package main\n\nfunc main() {}\n"), 0o644))

	planned := filepath.Join(dir, "users_gen.go")
	plannedInfo, plannedErr := os.Lstat(planned)
	foreignInfo, foreignErr := os.Lstat(foreign)
	if plannedErr == nil && foreignErr == nil && os.SameFile(plannedInfo, foreignInfo) {
		t.Skip("filesystem ignores case in file names")
	}

	store := generate.Store{Package: "store", Root: root, Dir: "gen", Tables: []schema.TableDef{usersTableDef()}}
	_, err := store.Plan()
	require.ErrorContains(t, err, `already holds package "main"`)
	require.ErrorContains(t, err, "USERS_gen.go")
}

func TestStorePlanRefusesAPackageEnabledByGOFLAGS(t *testing.T) {
	t.Setenv("GOFLAGS", "-tags=rasqlgen_test_tools")

	root := t.TempDir()
	dir := filepath.Join(root, "gen")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	foreign := filepath.Join(dir, "tools.go")
	require.NoError(t, os.WriteFile(foreign, []byte("//go:build rasqlgen_test_tools\n\npackage tools\n"), 0o644))

	store := generate.Store{Package: "store", Root: root, Dir: "gen", Tables: []schema.TableDef{usersTableDef()}}
	_, err := store.Plan()
	require.ErrorContains(t, err, `already holds package "tools"`)
	require.ErrorContains(t, err, "tools.go")
}

// TestStorePlanAcceptsADirectoryNoOtherPackageOwns is the control for the
// test above. Each case below puts a file in Dir that the check has to let
// through, and getting any of them wrong turns a working layout into a
// refused run.
func TestStorePlanAcceptsADirectoryNoOtherPackageOwns(t *testing.T) {
	testCases := map[string]struct {
		name    string
		content string
	}{
		// The conventional Go generator: a package main program kept beside
		// the package it generates, excluded from it by a build constraint.
		// The constraint is what makes the two packages one directory's
		// worth of legal Go, so the check has to read it.
		"a build-excluded program": {
			name:    "tools.go",
			content: "//go:build ignore\n\npackage main\n\nfunc main() {}\n",
		},
		// A hand-written file of the store's own package.
		"a file of the same package": {
			name:    "helpers.go",
			content: "package store\n\nconst helper = 1\n",
		},
		// The external test package is the one second package clause a Go
		// directory is allowed.
		"an external test package": {
			name:    "helpers_test.go",
			content: "package store_test\n",
		},
		// A file rasqlgen wrote under the package's former name. A run that
		// renames the generated package rewrites it, so refusing it here
		// would refuse the rename itself.
		"a generated file of the former package": {
			name:    "users_gen.go",
			content: genfile.Marker + "\n\npackage oldstore\n",
		},
		// Not Go source at all.
		"a file that is not Go source": {
			name:    "README.md",
			content: "package nothing\n",
		},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "gen")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, testCase.name), []byte(testCase.content), 0o644))

			store := generate.Store{Package: "store", Root: root, Dir: "gen", Tables: []schema.TableDef{usersTableDef()}}
			require.NoError(t, store.Write())

			source, err := os.ReadFile(filepath.Join(dir, "users_gen.go"))
			require.NoError(t, err)
			require.Contains(t, string(source), "package store\n")
		})
	}
}
