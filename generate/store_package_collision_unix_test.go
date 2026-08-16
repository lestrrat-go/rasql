//go:build unix

package generate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// These tests use Unix-only symbolic links.

// TestStorePlanRefusesAnotherPackageReachedThroughALink is
// TestStorePlanRefusesADirectoryHoldingAnotherPackage against the form the
// collision usually arrives in. main.go here is a symbolic link rather than a
// plain file, and go/build follows it and compiles the regular file it names,
// so the directory holds two packages exactly as a plain main.go would and
// `go build` reports "found packages main (main.go) and store (schema_gen.go)".
//
// The target sits outside Dir on purpose. That is where such a link points in
// practice, and it is the case an open directory handle cannot resolve on its
// own: os.Root refuses any link leaving the root, so a check that stats and
// reads only through the handle sees nothing here and lets the run write.
func TestStorePlanRefusesAnotherPackageReachedThroughALink(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "gen")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	outside := t.TempDir()
	target := filepath.Join(outside, "realmain.go")
	require.NoError(t, os.WriteFile(target, []byte("package main\n\nfunc main() {}\n"), 0o644))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "main.go")))

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

// TestStorePlanAcceptsALinkNoBuildCompiles is the control for the test above.
// Following a link must not turn every link in Dir into a refusal: only a link
// landing on a regular file the build compiles can collide, and each case here
// is a link the toolchain passes over. Reporting a collision for any of them,
// or failing over one, refuses a directory that builds perfectly well.
func TestStorePlanAcceptsALinkNoBuildCompiles(t *testing.T) {
	testCases := map[string]func(t *testing.T, root, dir string){
		// A link to a directory. go/build lists a directory's entries and
		// compiles none of them, and the name ending in .go changes nothing.
		"a link to a directory": func(t *testing.T, root, dir string) {
			target := filepath.Join(root, "elsewhere")
			require.NoError(t, os.MkdirAll(target, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(target, "main.go"), []byte("package main\n"), 0o644))
			require.NoError(t, os.Symlink(target, filepath.Join(dir, "sub.go")))
		},
		// A link naming nothing at all. There are no bytes to read a package
		// clause out of, and the build has nothing to compile either.
		"a dangling link": func(t *testing.T, root, dir string) {
			require.NoError(t, os.Symlink(filepath.Join(root, "gone", "main.go"), filepath.Join(dir, "main.go")))
		},
		// A link that does land on a regular file, declaring the store's own
		// package. Followed and compared like any other file, and the answer
		// is that it belongs here.
		"a link to a file of the same package": func(t *testing.T, root, dir string) {
			target := filepath.Join(root, "helpers.go")
			require.NoError(t, os.WriteFile(target, []byte("package store\n\nconst helper = 1\n"), 0o644))
			require.NoError(t, os.Symlink(target, filepath.Join(dir, "helpers.go")))
		},
	}
	for name, setup := range testCases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "gen")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			setup(t, root, dir)

			store := generate.Store{Package: "store", Root: root, Dir: "gen", Tables: []schema.TableDef{usersTableDef()}}
			require.NoError(t, store.Write())

			source, err := os.ReadFile(filepath.Join(dir, "users_gen.go"))
			require.NoError(t, err)
			require.Contains(t, string(source), "package store\n")
		})
	}
}
