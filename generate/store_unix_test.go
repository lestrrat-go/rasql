//go:build unix

package generate_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// This test uses Unix-only symbolic links.

// TestStorePlanReportsWhereASymlinkedFileResolves pins what File.Resolved is
// for: a planned destination that is a symbolic link pointing out of Dir is
// followed, not refused -- internal/genfile.Write writes through an output
// link on purpose -- so the plan reports the file a commit would actually
// replace instead of leaving File.Path to imply the bytes stay in Dir.
func TestStorePlanReportsWhereASymlinkedFileResolves(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "store")
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.MkdirAll(outside, 0o700))

	escaped := filepath.Join(outside, "escaped_q_gen.go")
	link := filepath.Join(dir, "q_gen.go")
	require.NoError(t, os.Symlink(escaped, link))

	sqlPath := filepath.Join(base, "q.sql")
	require.NoError(t, os.WriteFile(sqlPath, []byte("SELECT 1"), 0o600))

	store := generate.Store{
		Package: "store",
		Dir:     dir,
		Tables:  []schema.TableDef{usersTableDef()},
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{{Input: sqlPath, Function: "Q", Output: "q_gen.go"}},
	}
	plan, err := store.Plan()
	require.NoError(t, err)

	// The temporary directory itself may be reached through a link, so the
	// destinations that resolve to themselves are spelled the way the
	// filesystem reaches them rather than the way the test spelled them.
	realDir, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	realOutside, err := filepath.EvalSymlinks(outside)
	require.NoError(t, err)

	files := make(map[string]generate.File, len(plan.Files()))
	for _, f := range plan.Files() {
		files[filepath.Base(f.Path)] = f
	}
	require.Len(t, files, 4)

	query := files["q_gen.go"]
	require.Equal(t, link, query.Path, "Path is the destination as planned, inside Dir")
	require.Equal(t, filepath.Join(realOutside, "escaped_q_gen.go"), query.Resolved, "Resolved is the file the link points at")
	require.NotEqual(t, query.Path, query.Resolved)

	for name, f := range files {
		if name == "q_gen.go" {
			continue
		}
		require.Equal(t, filepath.Join(realDir, name), f.Resolved, "an ordinary destination resolves to itself")
	}

	// Planning still writes nothing: the link is untouched and its target
	// was not created, and the link is not a leftover to prune either.
	info, err := os.Lstat(link)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&fs.ModeSymlink, "Plan must leave the symbolic link a symbolic link")
	require.NoFileExists(t, escaped, "Plan must not create the file the link points at")
	require.Empty(t, plan.Orphans())
}

// TestStorePlanRejectsTwoFilesThatResolveToOneDestination covers two planned
// files whose distinct names inside Dir are both symbolic links to one file.
// The name check cannot see this -- "one_gen.go" and "two_gen.go" are
// distinct keys -- so the plan used to come back with no error, two files,
// and one destination a commit would write twice and keep only the last of.
// What is refused is the shared destination alone: the single-link case in
// TestStorePlanReportsWhereASymlinkedFileResolves still plans, links included.
func TestStorePlanRejectsTwoFilesThatResolveToOneDestination(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "store")
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.MkdirAll(outside, 0o700))

	target := filepath.Join(outside, "same_gen.go")
	one := filepath.Join(dir, "one_gen.go")
	two := filepath.Join(dir, "two_gen.go")
	require.NoError(t, os.Symlink(target, one))
	require.NoError(t, os.Symlink(target, two))

	sqlPath := filepath.Join(base, "q.sql")
	require.NoError(t, os.WriteFile(sqlPath, []byte("SELECT 1"), 0o600))

	store := generate.Store{
		Package: "store",
		Dir:     dir,
		Tables:  []schema.TableDef{usersTableDef()},
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{
			{Input: sqlPath, Function: "One", Output: "one_gen.go"},
			{Input: sqlPath, Function: "Two", Output: "two_gen.go"},
		},
	}
	plan, err := store.Plan()

	// The temporary directory itself may be reached through a link, so the
	// destination is spelled the way the filesystem reaches it.
	realOutside, evalErr := filepath.EvalSymlinks(outside)
	require.NoError(t, evalErr)
	require.ErrorContains(t, err, "both resolve to "+filepath.Join(realOutside, "same_gen.go"))
	require.ErrorContains(t, err, one)
	require.ErrorContains(t, err, two)
	require.Empty(t, plan.Files(), "a rejected Store must plan no files at all")
	require.NoFileExists(t, target, "Plan must not create the file the links point at")
}
