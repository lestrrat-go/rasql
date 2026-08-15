//go:build unix

package generate_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// These tests use Unix-only symbolic links. They cover what Check makes of a
// planned destination that resolves out of Dir, which is the one place a
// missing directory is not the plan's own.

// TestStoreCheckRefusesADestinationWhoseOutsideDirectoryIsMissing separates
// the one missing directory Check reports as staleness from the one it must
// not. Commit's step 1 creates each missing component of Dir's own path, so
// a planned file resolving into a Dir nothing has created is a file Commit
// would go on to write: staleness. A planned file that is a symbolic link
// into some other directory that does not exist is not that, because Commit
// creates nothing outside Dir; Commit refuses the whole run before writing
// anything, and rerunning the generator refuses it again.
//
// The refusal is compared with Commit's own error rather than only checked
// for its shape, since what Check promises is the answer a Write would give.
func TestStoreCheckRefusesADestinationWhoseOutsideDirectoryIsMissing(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "store")
	store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef()}}
	require.NoError(t, store.Write())

	outside := filepath.Join(base, "missing-parent")
	usersPath := filepath.Join(dir, "users_gen.go")
	require.NoError(t, os.Remove(usersPath))
	require.NoError(t, os.Symlink(filepath.Join(outside, "users_gen.go"), usersPath))
	require.NoDirExists(t, outside)

	before := snapshotTree(t, base)
	err := store.Check()
	require.Error(t, err)
	require.False(t, errors.Is(err, generate.ErrStale), "a destination whose directory no Commit creates is a refusal, not staleness")
	require.ErrorContains(t, err, outside)
	require.Equal(t, before, snapshotTree(t, base), "a Check that refused must not write, delete, or create anything")

	commitErr := store.Write()
	require.Error(t, commitErr)
	require.Equal(t, commitErr.Error(), err.Error(), "Check must report the refusal Commit gives, not a staleness a rerun would clear")
	require.NoDirExists(t, outside, "Commit creates nothing outside Dir, which is why this is a refusal")
	require.Equal(t, before, snapshotTree(t, base), "the Write that refused must not have written anything either")
}

// TestStoreCheckReadsThroughAPlannedSymlinkOutOfDir pins the case the
// refusal above must not swallow: a planned destination that is a symbolic
// link out of Dir into a directory that does exist is written there by
// Commit, so Check reads it there too -- clean right after a Write, and
// stale, naming the planned path, once those bytes change.
func TestStoreCheckReadsThroughAPlannedSymlinkOutOfDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "store")
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.MkdirAll(outside, 0o700))

	escaped := filepath.Join(outside, "escaped_users_gen.go")
	usersPath := filepath.Join(dir, "users_gen.go")
	require.NoError(t, os.Symlink(escaped, usersPath))

	store := generate.Store{Package: "store", Dir: dir, Tables: []schema.TableDef{usersTableDef()}}
	require.NoError(t, store.Write())
	require.FileExists(t, escaped)
	require.NoError(t, store.Check())

	original, err := os.ReadFile(escaped)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(escaped, append(original, []byte("\n// a hand edit that keeps the marker\n")...), 0o600))

	err = store.Check()
	require.Error(t, err)
	require.True(t, errors.Is(err, generate.ErrStale))
	require.ErrorContains(t, err, usersPath)
	require.ErrorContains(t, err, "differs")
}
