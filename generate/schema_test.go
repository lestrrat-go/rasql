package generate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// packageUsage and tableUsage call what each helper's output promises a
// caller, from inside the compiled package, so an entry point that is
// declared but unreachable fails here too.
const (
	packageUsage = `package pkgsource

import "github.com/lestrrat-go/rasql/query"

var _ query.ColumnRef = Users().ID()
var _ query.ColumnRef = Orders().ID()
var _ = UsersDef()
var _ = Orders().User()
`
	tableUsage = `package tablesource

import "github.com/lestrrat-go/rasql/query"

var _ query.ColumnRef = Users().Email()
var _ = UsersDef()
`
)

// TestExportedSourceCompilesAlone pins what PackageSource and TableSource
// return: a whole compilation unit each. Every helper's output is written as
// the only generated file of its own package, so a declaration that reaches
// for something the file does not declare fails to compile here rather than
// in a caller's tree.
//
// The two packages are built in one module, which is also what keeps them
// honest: a package that quietly borrowed a declaration from the other would
// still not compile, since Go resolves nothing across package boundaries
// unqualified.
func TestExportedSourceCompilesAlone(t *testing.T) {
	users := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}, Nullable: true},
			{Name: "created_at", Type: schema.TimeType{}},
		},
		PrimaryKey: []string{"id"},
	}
	orders := schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Columns:           []string{"user_id"},
			ReferencedTable:   "users",
			ReferencedColumns: []string{"id"},
		}},
	}

	packageSource, err := generate.PackageSource("pkgsource", users, orders)
	require.NoError(t, err)
	// TableSource emits one table, and a relationship method names the
	// generated type of the table at its other end, so the table stands
	// alone only when its relationships do not reach outside it.
	tableSource, err := generate.TableSource("tablesource", users)
	require.NoError(t, err)

	directory := t.TempDir()
	repository, err := filepath.Abs("..")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"),
		[]byte("module example.com/generated\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => "+filepath.ToSlash(repository)+"\n"), 0o600))

	for _, generated := range []struct {
		directory string
		source    []byte
		usage     string
	}{
		{directory: "pkgsource", source: packageSource, usage: packageUsage},
		{directory: "tablesource", source: tableSource, usage: tableUsage},
	} {
		target := filepath.Join(directory, generated.directory)
		require.NoError(t, os.MkdirAll(target, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(target, "generated.go"), generated.source, 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(target, "usage.go"), []byte(generated.usage), 0o600))
	}

	command := exec.CommandContext(t.Context(), "go", "mod", "tidy")
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "go mod tidy output:\n%s", output)

	command = exec.CommandContext(t.Context(), "go", "build", "./...")
	command.Dir = directory
	output, err = command.CombinedOutput()
	require.NoErrorf(t, err, "go build output:\n%s", output)
}

// TestDescriptorSourceIsTheSplitFileCounterpart states the other side of the
// same contract. DescriptorSource writes the declarations PackageSource and
// TableSource already carry, so it belongs beside the per-table files
// rasqlgen writes, never beside theirs.
func TestDescriptorSourceIsTheSplitFileCounterpart(t *testing.T) {
	users := schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}

	packageSource, err := generate.PackageSource("generated", users)
	require.NoError(t, err)
	tableSource, err := generate.TableSource("generated", users)
	require.NoError(t, err)
	descriptorSource, err := generate.DescriptorSource("generated", users)
	require.NoError(t, err)

	for _, declaration := range []string{
		"var usersDef = schema.TableDef{",
		"var usersTable = UsersTable{rasql.TableFrom[UsersRow](usersDef)}",
		"func UsersDef() schema.TableDef { return usersDef.Clone() }",
	} {
		require.Contains(t, string(descriptorSource), declaration)
		require.Contains(t, string(packageSource), declaration)
		require.Contains(t, string(tableSource), declaration)
	}
}

func TestValidateReportsCollisionsAcrossTables(t *testing.T) {
	err := generate.Validate("generated",
		schema.TableDef{
			Name:       "users",
			Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
			PrimaryKey: []string{"id"},
		},
		schema.TableDef{
			Name:       "users_table",
			Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
			PrimaryKey: []string{"id"},
		},
	)
	require.ErrorContains(t, err, `duplicates generated name "UsersTable"`)
}

// TestValidateRejectsInvalidPackageNames covers every value Validate
// refuses as a package name, since it is the one exported check both
// WritePackage (the `rasqlgen schema` path) and a library caller building
// its own source with PackageSource route through.
func TestValidateRejectsInvalidPackageNames(t *testing.T) {
	users := schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}
	for _, testCase := range []struct {
		name  string
		value string
	}{
		{"blank_identifier", "_"},
		{"keyword", "func"},
		{"starts_with_digit", "2fast"},
		{"contains_hyphen", "not-valid"},
		{"empty_string", ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := generate.Validate(testCase.value, users)
			require.ErrorContains(t, err, "invalid package name")
		})
	}
}

// TestValidateAcceptsUsablePackageNames guards the check against being
// over-broad.
func TestValidateAcceptsUsablePackageNames(t *testing.T) {
	users := schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}
	for _, packageName := range []string{"generated", "init", "main"} {
		t.Run(packageName, func(t *testing.T) {
			require.NoError(t, generate.Validate(packageName, users))
		})
	}
}

func TestDescriptorTestSourceNamesTheGenerator(t *testing.T) {
	users := schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}

	source, err := generate.DescriptorTestSource("generated", users)
	require.NoError(t, err)
	require.Contains(t, string(source), "func TestRasqlgenGeneratedDefinitionsAreValid(t *testing.T) {")
}
