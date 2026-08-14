package rasqlgen_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	"github.com/stretchr/testify/require"
)

// fixtureTablesSource is the schema package every -source fixture module
// starts from: two small tables, kept small so the compile each test pays
// for stays fast.
const fixtureTablesSource = `package tables

import "github.com/lestrrat-go/rasql/schema"

func Tables() []schema.TableDef {
	return []schema.TableDef{
		schema.MustTableDef("users",
			schema.Integer("id"),
			schema.Text("email"),
			schema.PrimaryKey("id"),
		),
		schema.MustTableDef("orders",
			schema.Integer("id"),
			schema.PrimaryKey("id"),
		),
	}
}
`

// newSchemaSourceFixture builds a scratch Go module in t.TempDir() holding
// a schema package under internal/tables (not a top-level directory, so
// this single fixture covers the case that forces the temporary directory
// rasqlgen writes to be in-tree: a package under internal/ cannot be
// imported from outside its own module) and an empty internal/store output
// directory. tablesSource replaces the package body, so failure-case tests
// can supply a schema package that does not compile.
//
// Copying the repository's own go.mod, rather than writing "go 1.26" by
// hand, is what supplies the exact "go 1.26.0" directive: measured on
// go1.26.1, a hand-written two-component "go 1.26" makes every `go run`
// fail with "go: updates to go.mod needed; to update it: go mod tidy".
// With the copied directive, no go.sum and no `go mod tidy` are needed and
// the whole fixture runs under GOPROXY=off. This deliberately does not
// follow rasqlgen_e2e_test.go's TestGoRunSchemaGeneratesCompilableSource,
// which runs `go get ...@v0.0.0` first and reaches the network; the
// replace directive below makes that step unnecessary.
func newSchemaSourceFixture(t *testing.T, tablesSource string) string {
	t.Helper()
	moduleDir := t.TempDir()

	repoGoMod, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	require.NoError(t, err)
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	module := strings.Replace(string(repoGoMod), "module github.com/lestrrat-go/rasql\n", "module example.com/consumer\n", 1)
	module += "\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte(module), 0o600))

	tablesDir := filepath.Join(moduleDir, "internal", "tables")
	require.NoError(t, os.MkdirAll(tablesDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(tablesDir, "tables.go"), []byte(tablesSource), 0o600))

	require.NoError(t, os.MkdirAll(filepath.Join(moduleDir, "internal", "store"), 0o700))

	return moduleDir
}

// requireNoLeftoverSourceTempDir asserts that no directory anywhere under
// root's tree starts with ".rasqlgen-source-", which is the prefix
// runSchemaSource gives the temporary module it writes, runs, and removes.
// Walking the whole tree, not just root itself, catches a leftover placed
// under the wrong parent directory.
func requireNoLeftoverSourceTempDir(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && strings.HasPrefix(entry.Name(), ".rasqlgen-source-") {
			t.Errorf("leftover temporary directory %s", path)
		}
		return nil
	})
	require.NoError(t, err)
}

// This test spawns a real `go run`.
func TestRunSchemaSourceSuccess(t *testing.T) {
	moduleDir := newSchemaSourceFixture(t, fixtureTablesSource)
	t.Chdir(moduleDir)

	var buffer bytes.Buffer
	require.NoError(t, rasqlgen.Run([]string{"schema", "-source", "./internal/tables", "-package", "store", "-output", "internal/store"}, &buffer))

	usersSource, err := os.ReadFile(filepath.Join(moduleDir, "internal", "store", "users_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(usersSource), "func Users() UsersTable {")
	ordersSource, err := os.ReadFile(filepath.Join(moduleDir, "internal", "store", "orders_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(ordersSource), "func Orders() OrdersTable {")

	requireNoLeftoverSourceTempDir(t, moduleDir)
}

// This test spawns a real `go run`. README.md:39 documents -source with a
// bare relative directory, no "./" prefix; this is the form that used to
// take the slower package-pattern path through the whole module graph
// instead of the directory-pattern path, and must succeed the same way
// TestRunSchemaSourceSuccess does.
func TestRunSchemaSourceAcceptsABareRelativeDirectory(t *testing.T) {
	moduleDir := newSchemaSourceFixture(t, fixtureTablesSource)
	t.Chdir(moduleDir)

	var buffer bytes.Buffer
	require.NoError(t, rasqlgen.Run([]string{"schema", "-source", "internal/tables", "-package", "store", "-output", "internal/store"}, &buffer))

	usersSource, err := os.ReadFile(filepath.Join(moduleDir, "internal", "store", "users_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(usersSource), "func Users() UsersTable {")
	ordersSource, err := os.ReadFile(filepath.Join(moduleDir, "internal", "store", "orders_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(ordersSource), "func Orders() OrdersTable {")

	requireNoLeftoverSourceTempDir(t, moduleDir)
}

// This test spawns a real `go run`. A schema package that does not compile
// must fail the call and forward the child's own compiler message; the
// temporary directory must still be removed.
func TestRunSchemaSourceCompileFailureForwardsChildMessage(t *testing.T) {
	tablesSource := `package tables

import "github.com/lestrrat-go/rasql/schema"

func Tables() []schema.TableDef {
	return undefinedThing()
}
`
	moduleDir := newSchemaSourceFixture(t, tablesSource)
	t.Chdir(moduleDir)

	var buffer bytes.Buffer
	err := rasqlgen.Run([]string{"schema", "-source", "./internal/tables", "-package", "store", "-output", "internal/store"}, &buffer)
	require.Error(t, err)
	require.Contains(t, buffer.String(), "undefined: undefinedThing")

	requireNoLeftoverSourceTempDir(t, moduleDir)
}

// This test spawns a real `go run`. A schema package missing Tables must
// fail with the compiler's own undefined-symbol message.
func TestRunSchemaSourceMissingTablesFunction(t *testing.T) {
	tablesSource := strings.Replace(fixtureTablesSource, "func Tables()", "func Other()", 1)
	moduleDir := newSchemaSourceFixture(t, tablesSource)
	t.Chdir(moduleDir)

	var buffer bytes.Buffer
	err := rasqlgen.Run([]string{"schema", "-source", "./internal/tables", "-package", "store", "-output", "internal/store"}, &buffer)
	require.Error(t, err)
	require.Contains(t, buffer.String(), "undefined: schemasource.Tables")

	requireNoLeftoverSourceTempDir(t, moduleDir)
}

// This test spawns a real `go run`. A Tables with the wrong signature must
// fail to compile the generated program, quoting the mismatched type.
func TestRunSchemaSourceWrongTablesSignature(t *testing.T) {
	tablesSource := `package tables

func Tables() []string {
	return nil
}
`
	moduleDir := newSchemaSourceFixture(t, tablesSource)
	t.Chdir(moduleDir)

	var buffer bytes.Buffer
	err := rasqlgen.Run([]string{"schema", "-source", "./internal/tables", "-package", "store", "-output", "internal/store"}, &buffer)
	require.Error(t, err)
	require.Contains(t, buffer.String(), "variable of type []string")
	require.Contains(t, buffer.String(), "as []schema.TableDef value")

	requireNoLeftoverSourceTempDir(t, moduleDir)
}

// This test spawns a real `go run`. Tables returning no tables must be
// reported by the child program, and nothing must be written.
func TestRunSchemaSourceNoTablesReturned(t *testing.T) {
	tablesSource := `package tables

import "github.com/lestrrat-go/rasql/schema"

func Tables() []schema.TableDef {
	return nil
}
`
	moduleDir := newSchemaSourceFixture(t, tablesSource)
	t.Chdir(moduleDir)

	var buffer bytes.Buffer
	err := rasqlgen.Run([]string{"schema", "-source", "./internal/tables", "-package", "store", "-output", "internal/store"}, &buffer)
	require.Error(t, err)
	require.Contains(t, buffer.String(), "returned no tables")
	entries, readErr := os.ReadDir(filepath.Join(moduleDir, "internal", "store"))
	require.NoError(t, readErr)
	require.Empty(t, entries)

	requireNoLeftoverSourceTempDir(t, moduleDir)
}

// This test spawns a real `go run`. -table filters which generated files
// are written, the same as it does for -dsn.
func TestRunSchemaSourceTableFlagFilters(t *testing.T) {
	moduleDir := newSchemaSourceFixture(t, fixtureTablesSource)
	t.Chdir(moduleDir)

	var buffer bytes.Buffer
	require.NoError(t, rasqlgen.Run([]string{"schema", "-source", "./internal/tables", "-table", "users", "-package", "store", "-output", "internal/store"}, &buffer))

	require.FileExists(t, filepath.Join(moduleDir, "internal", "store", "users_gen.go"))
	require.NoFileExists(t, filepath.Join(moduleDir, "internal", "store", "orders_gen.go"))

	requireNoLeftoverSourceTempDir(t, moduleDir)
}

// This test spawns a real `go run`. -table naming a table the source does
// not have must fail with the child's own message, and nothing is written.
func TestRunSchemaSourceTableFlagNamesMissingTable(t *testing.T) {
	moduleDir := newSchemaSourceFixture(t, fixtureTablesSource)
	t.Chdir(moduleDir)

	var buffer bytes.Buffer
	err := rasqlgen.Run([]string{"schema", "-source", "./internal/tables", "-table", "missing", "-package", "store", "-output", "internal/store"}, &buffer)
	require.Error(t, err)
	require.Contains(t, buffer.String(), `schema source has no table "missing"`)
	entries, readErr := os.ReadDir(filepath.Join(moduleDir, "internal", "store"))
	require.NoError(t, readErr)
	require.Empty(t, entries)

	requireNoLeftoverSourceTempDir(t, moduleDir)
}

// This test spawns a real `go run` (go list, specifically). A directory
// that is not a Go package must fail with go list's own stderr message,
// and no temporary directory is left behind.
func TestRunSchemaSourceDirectoryNotAPackage(t *testing.T) {
	moduleDir := newSchemaSourceFixture(t, fixtureTablesSource)
	t.Chdir(moduleDir)

	var buffer bytes.Buffer
	err := rasqlgen.Run([]string{"schema", "-source", "./nowhere", "-package", "store", "-output", "internal/store"}, &buffer)
	require.Error(t, err)
	require.ErrorContains(t, err, "directory not found")

	requireNoLeftoverSourceTempDir(t, moduleDir)
}

// TestRunSchemaSourceFlagExclusivity needs no fixture module: flag parsing
// happens before the source directory is ever resolved.
func TestRunSchemaSourceFlagExclusivity(t *testing.T) {
	directory := t.TempDir()

	var buffer bytes.Buffer
	err := rasqlgen.Run([]string{"schema", "-source", "./internal/tables", "-dsn", "postgres://example", "-table", "users", "-package", "generated", "-output", directory}, &buffer)
	require.EqualError(t, err, "schema accepts one of -dsn or -source, not both")
	require.NotEqual(t, "schema requires one of -dsn or -source", err.Error())
}

// TestRunSchemaSourceTemporaryDirectoryDoesNotDisturbModuleListing pairs a
// positive assertion with a negative control, which is what documents why
// the dot prefix on the temporary directory's name is load-bearing: without
// the control, the positive assertion alone would pass even if the prefix
// were dropped and the directory simply removed in time.
func TestRunSchemaSourceTemporaryDirectoryDoesNotDisturbModuleListing(t *testing.T) {
	moduleDir := newSchemaSourceFixture(t, fixtureTablesSource)
	t.Chdir(moduleDir)

	var buffer bytes.Buffer
	require.NoError(t, rasqlgen.Run([]string{"schema", "-source", "./internal/tables", "-package", "store", "-output", "internal/store"}, &buffer))

	listed := runGoListAll(t, moduleDir)
	for _, path := range listed {
		require.NotContains(t, path, "rasqlgen-source")
	}

	// Negative control: a plain (non-dot) directory holding the same
	// package main IS listed by go list ./..., which is what a temp
	// directory without the dot prefix would have exposed to a concurrent
	// go build ./... or go vet ./....
	plainDir := filepath.Join(moduleDir, "rasqlgen_load_control")
	require.NoError(t, os.MkdirAll(plainDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(plainDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600))
	listed = runGoListAll(t, moduleDir)
	found := false
	for _, path := range listed {
		if strings.Contains(path, "rasqlgen_load_control") {
			found = true
		}
	}
	require.True(t, found, "expected go list ./... to list the plain control directory, got %v", listed)
}

func runGoListAll(t *testing.T, moduleDir string) []string {
	t.Helper()
	// -buildvcs=false: the fixture module lives under t.TempDir(), which
	// can land inside an unrelated enclosing VCS checkout depending on
	// where the OS puts temporary directories; go list's VCS stamping then
	// fails with "error obtaining VCS status" for reasons unrelated to
	// what this test checks.
	cmd := exec.CommandContext(t.Context(), "go", "list", "-buildvcs=false", "./...")
	cmd.Dir = moduleDir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	return strings.Fields(string(output))
}
