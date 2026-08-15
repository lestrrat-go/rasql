package rasqlgen

import (
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
