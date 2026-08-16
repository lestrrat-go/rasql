//go:build unix

package generate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/stretchr/testify/require"
)

func TestQueryPackagePlanRejectsCaseVariantSymlinkDestinations(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "store")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	writeQueryPackageDestinationInput(t, root, "one.sql")
	writeQueryPackageDestinationInput(t, root, "two.sql")
	require.NoError(t, os.Symlink("a_gen.go", filepath.Join(dir, "one_gen.go")))
	require.NoError(t, os.Symlink("A_gen.go", filepath.Join(dir, "two_gen.go")))

	queries := QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.SQLite(),
		Queries: []Query{
			{Input: "one.sql", Function: "One", Output: "one_gen.go"},
			{Input: "two.sql", Function: "Two", Output: "two_gen.go"},
		},
	}

	_, err := queries.Plan()
	require.ErrorContains(t, err, "both resolve")
	require.NoFileExists(t, filepath.Join(dir, "a_gen.go"))
	require.NoFileExists(t, filepath.Join(dir, "A_gen.go"))
}

func TestQueryPlanRejectsCaseVariantSymlinkDestinationsBeforeReadingOrWriting(t *testing.T) {
	for _, method := range []struct {
		name string
		call func(QueryPlan) error
	}{
		{name: "Check", call: QueryPlan.Check},
		{name: "Commit", call: QueryPlan.Commit},
	} {
		t.Run(method.name, func(t *testing.T) {
			plan := newCaseVariantQueryPlan(t)
			err := method.call(plan)
			require.ErrorContains(t, err, "both resolve")
			require.NoFileExists(t, filepath.Join(plan.dir, "a_gen.go"))
			require.NoFileExists(t, filepath.Join(plan.dir, "A_gen.go"))
		})
	}
}

func newCaseVariantQueryPlan(t *testing.T) QueryPlan {
	t.Helper()
	dir := t.TempDir()
	first := filepath.Join(dir, "one_gen.go")
	second := filepath.Join(dir, "two_gen.go")
	firstDestination := filepath.Join(dir, "a_gen.go")
	secondDestination := filepath.Join(dir, "A_gen.go")
	require.NoError(t, os.Symlink("a_gen.go", first))
	require.NoError(t, os.Symlink("A_gen.go", second))
	info, err := os.Stat(dir)
	require.NoError(t, err)
	return QueryPlan{
		files: []File{
			{Path: first, Resolved: firstDestination, Source: []byte(genfile.Marker + "\n\npackage store\n")},
			{Path: second, Resolved: secondDestination, Source: []byte(genfile.Marker + "\n\npackage store\n")},
		},
		packageName: "store",
		dir:         dir,
		anchor:      dir,
		anchorInfo:  info,
	}
}

func writeQueryPackageDestinationInput(t *testing.T, root, name string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("SELECT 1"), 0o600))
}
