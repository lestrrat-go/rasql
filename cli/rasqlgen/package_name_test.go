package rasqlgen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// invalidCLIPackageNames enumerates every -package value `schema` and
// `bootstrap` refuse, and why: "_" is an ordinary Go identifier token
// everywhere else, but the language spec forbids the blank identifier as a
// PackageName, so it is checked on its own; a keyword, a name starting
// with a digit, and a name holding a character such as "-" are never Go
// identifiers at all, and token.IsIdentifier already refuses each of
// those. This is the reported defect (-package _ produced a package that
// does not compile) plus the same class of value, so a future change to
// the shared check in internal/schemagen cannot silently narrow it back
// down to just the one reported case.
var invalidCLIPackageNames = []struct {
	name  string
	value string
}{
	{"blank_identifier", "_"},
	{"keyword_func", "func"},
	{"keyword_range", "range"},
	{"starts_with_digit", "2fast"},
	{"contains_hyphen", "foo-bar"},
}

// TestRunSchemaRejectsInvalidPackageName pins that `rasqlgen schema`
// refuses every value internal/schemagen refuses, through the actual
// command-line path (runSchema -> generate.WritePackage), not just the
// library call it wraps: this is what let `-package _` reach
// generate.WritePackage unchecked before internal/schemagen gained its own
// blank-identifier guard.
func TestRunSchemaRejectsInvalidPackageName(t *testing.T) {
	databasePath := mustCreateSQLite(t, "CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT NOT NULL)")
	for _, testCase := range invalidCLIPackageNames {
		t.Run(testCase.name, func(t *testing.T) {
			directory := t.TempDir()
			err := run([]string{"schema", "-dsn", databasePath, "-dialect", "sqlite", "-table", "widgets", "-package", testCase.value, "-output", directory})
			require.ErrorContains(t, err, "invalid package name")
			require.ErrorContains(t, err, testCase.value)
			require.Empty(t, mustReadDir(t, directory))
		})
	}
}

// TestRunBootstrapRejectsInvalidPackageName is TestRunSchemaRejectsInvalidPackageName's
// counterpart for `rasqlgen bootstrap`, which reaches the same guard
// through a different generate entry point (generate.WriteDescriptionPackage).
func TestRunBootstrapRejectsInvalidPackageName(t *testing.T) {
	databasePath := mustCreateSQLite(t, "CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT NOT NULL)")
	for _, testCase := range invalidCLIPackageNames {
		t.Run(testCase.name, func(t *testing.T) {
			directory := t.TempDir()
			err := run([]string{"bootstrap", "-dsn", databasePath, "-dialect", "sqlite", "-table", "widgets", "-package", testCase.value, "-output", directory})
			require.ErrorContains(t, err, "invalid package name")
			require.ErrorContains(t, err, testCase.value)
			require.Empty(t, mustReadDir(t, directory))
		})
	}
}

// TestRunBootstrapRefreshRejectsInvalidPackageName covers the other half
// of `rasqlgen bootstrap`: a re-run into an output directory bootstrap
// already owns, which is a refresh rather than a first write and so never
// reaches generate.WriteDescriptionPackage. Every refresh path is
// exercised, because each one used to reach its end without a single call
// that renders source with the given package name and therefore without a
// single call that checks it. A report-only run returned nil whether the
// diff was empty or not, and a -write run whose diff only removed tables
// deleted the dropped table's file first and failed afterward, leaving an
// aggregator calling a function no file declared.
func TestRunBootstrapRefreshRejectsInvalidPackageName(t *testing.T) {
	refreshes := []struct {
		name  string
		drift []string
		write bool
	}{
		{name: "empty_diff"},
		{name: "added_table", drift: []string{"CREATE TABLE sprockets (id INTEGER PRIMARY KEY)"}},
		{name: "removed_table_write", drift: []string{"DROP TABLE gadgets"}, write: true},
	}
	for _, testCase := range invalidCLIPackageNames {
		t.Run(testCase.name, func(t *testing.T) {
			for _, refresh := range refreshes {
				t.Run(refresh.name, func(t *testing.T) {
					databasePath := mustCreateSQLite(t,
						"CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT NOT NULL)",
						"CREATE TABLE gadgets (id INTEGER PRIMARY KEY)",
					)
					directory := t.TempDir()
					require.NoError(t, run([]string{"bootstrap", "-dsn", databasePath, "-dialect", "sqlite", "-package", "schemasource", "-output", directory}))
					mustExecSQLite(t, databasePath, refresh.drift...)
					before := mustSnapshotFiles(t, directory)

					args := []string{"bootstrap", "-dsn", databasePath, "-dialect", "sqlite", "-package", testCase.value, "-output", directory}
					if refresh.write {
						args = append(args, "-write")
					}
					err := run(args)
					require.ErrorContains(t, err, "invalid package name")
					require.ErrorContains(t, err, testCase.value)
					require.Equal(t, before, mustSnapshotFiles(t, directory),
						"a refused refresh must leave the package it already owns byte-identical")
				})
			}
		})
	}
}

// mustSnapshotFiles reads every regular file directly inside directory into
// a name-to-contents map, so a run that must change nothing can be checked
// byte for byte rather than by file names alone.
func mustSnapshotFiles(t *testing.T, directory string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	for _, entry := range mustReadDir(t, directory) {
		contents, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		require.NoError(t, err)
		snapshot[entry.Name()] = string(contents)
	}
	return snapshot
}

// TestRunSchemaAcceptsValidPackageName guards the check against being
// over-broad: an ordinary package name must still generate source that
// compiles.
func TestRunSchemaAcceptsValidPackageName(t *testing.T) {
	databasePath := mustCreateSQLite(t, "CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT NOT NULL)")
	directory := t.TempDir()
	err := run([]string{"schema", "-dsn", databasePath, "-dialect", "sqlite", "-table", "widgets", "-package", "widgets", "-output", directory})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(directory, "widgets_gen.go"))
}

// TestRunBootstrapAcceptsValidPackageName is
// TestRunSchemaAcceptsValidPackageName's counterpart for `rasqlgen bootstrap`.
func TestRunBootstrapAcceptsValidPackageName(t *testing.T) {
	databasePath := mustCreateSQLite(t, "CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT NOT NULL)")
	directory := t.TempDir()
	err := run([]string{"bootstrap", "-dsn", databasePath, "-dialect", "sqlite", "-table", "widgets", "-package", "widgets", "-output", directory})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(directory, "widgets_gen.go"))
}
