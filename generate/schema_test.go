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

var _ query.ColumnRef = Users().ID().Ref()
var _ query.ColumnRef = Orders().ID().Ref()
var _ = UsersDef()
var _ = Orders().User()
`
	tableUsage = `package tablesource

import "github.com/lestrrat-go/rasql/query"

var _ query.ColumnRef = Users().Email().Ref()
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

func TestPackageSourceCustomNullableBelongsToLoadsSQLite(t *testing.T) {
	users := schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}, GoBinding: &schema.GoBinding{Type: "ID"}}},
		PrimaryKey: []string{"id"},
	}
	profiles := schema.TableDef{
		Name: "profiles",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}, GoBinding: &schema.GoBinding{Type: "ID"}},
			{Name: "user_id", Type: schema.IntegerType{}, Nullable: true, GoBinding: &schema.GoBinding{Type: "ID", NullableType: "NullID"}},
		},
		PrimaryKey:        []string{"id"},
		UniqueConstraints: []schema.UniqueDef{{Columns: []string{"user_id"}}},
		ForeignKeys:       []schema.ForeignKeyDef{{Columns: []string{"user_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}}},
		Relationships: []schema.RelationshipDef{{
			Name:              "User",
			Kind:              schema.RelationshipBelongsTo,
			Optionality:       schema.RelationshipOptional,
			Columns:           []string{"user_id"},
			ReferencedTable:   "users",
			ReferencedColumns: []string{"id"},
		}},
	}
	source, err := generate.PackageSource("store", users, profiles)
	require.NoError(t, err)
	directory := t.TempDir()
	repository, err := filepath.Abs("..")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte("module example.com/customnullable\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\nrequire modernc.org/sqlite v1.55.0\n\nreplace github.com/lestrrat-go/rasql => "+filepath.ToSlash(repository)+"\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(directory, "generated"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "generated", "generated.go"), source, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "generated", "types.go"), []byte(customNullableTypes), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "generated", "custom_nullable_test.go"), []byte(customNullableRuntimeTest), 0o600))
	command := exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "./generated")
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "external custom nullable output:\n%s\n%s", output, source)
}

const customNullableTypes = `package store

import (
	"database/sql/driver"
	"fmt"
)

type ID int64
type NullID struct { Raw ID; Valid bool }

func (v *NullID) NullableBind() (any, bool) { return v.Raw, v.Valid }
func (v NullID) Value() (driver.Value, error) { if !v.Valid { return nil, nil }; return int64(v.Raw), nil }
func (v *NullID) Scan(value any) error {
	if value == nil { v.Raw, v.Valid = 0, false; return nil }
	switch value := value.(type) {
	case int64:
		v.Raw, v.Valid = ID(value), true
		return nil
	case int:
		v.Raw, v.Valid = ID(value), true
		return nil
	default:
		return fmt.Errorf("unexpected scan value %T", value)
	}
}
`

const customNullableRuntimeTest = `package store_test

import (
	"database/sql"
	"testing"

	store "example.com/customnullable/generated"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestCustomNullableRelationship(t *testing.T) {
	raw, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db, err := rasql.New(raw, dialect.SQLite())
	require.NoError(t, err)
	for _, statement := range []string{
		"CREATE TABLE users (id INTEGER PRIMARY KEY)",
		"CREATE TABLE profiles (id INTEGER PRIMARY KEY, user_id INTEGER UNIQUE)",
		"INSERT INTO users VALUES (1), (2)",
		"INSERT INTO profiles VALUES (10, 1), (11, NULL)",
	} {
		_, err = raw.ExecContext(t.Context(), statement)
		require.NoError(t, err)
	}
	present := store.ProfilesRow{ID: 10, UserID: store.NullID{Raw: 1, Valid: true}}
	empty := store.ProfilesRow{ID: 11, UserID: store.NullID{Valid: false}}
	belongs := store.Profiles().User()
	key := belongs.SourceKey(present)
	require.NotNil(t, key)
	require.Equal(t, store.ID(1), *key)
	require.Nil(t, belongs.SourceKey(empty))
	loaded, err := belongs.Load(t.Context(), db, []store.ProfilesRow{present, empty})
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	for key, row := range loaded {
		require.NotNil(t, key)
		require.Equal(t, store.ID(1), *key)
		require.Equal(t, store.ID(1), row.ID)
	}
	inverse := store.Users().Profiles()
	require.Equal(t, store.ID(1), inverse.SourceKey(store.UsersRow{ID: 1}))
	require.Equal(t, store.ID(1), inverse.TargetKey(present))
	inverseLoaded, err := inverse.Load(t.Context(), db, []store.UsersRow{{ID: 1}, {ID: 2}})
	require.NoError(t, err)
	require.Len(t, inverseLoaded, 1)
	require.Equal(t, store.ID(10), inverseLoaded[1].ID)
}
`

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
// WritePackage and a library caller building its own source with PackageSource
// route through.
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

func TestRelationshipValidationAndHasManyApplyToEverySourceEntryPoint(t *testing.T) {
	users := schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
		Relationships: []schema.RelationshipDef{{
			Name:              "Children",
			Kind:              schema.RelationshipHasMany,
			Optionality:       schema.RelationshipRequired,
			Columns:           []string{"id"},
			ReferencedTable:   "children",
			ReferencedColumns: []string{"user_id"},
		}},
	}
	children := schema.TableDef{
		Name: "children",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	}
	source, err := generate.PackageSource("generated", users, children)
	require.NoError(t, err)
	require.Contains(t, string(source), "func (t UsersTable) Children() UsersTableChildrenRelation")
	require.Contains(t, string(source), "LoadHasManyPlan")

	missing := users
	missing.Relationships[0].ReferencedTable = "missing"
	for name, call := range map[string]func() error{
		"PackageSource": func() error { _, err := generate.PackageSource("generated", missing, children); return err },
		"TableSource": func() error {
			_, err := generate.TableSource("generated", missing, missing, children)
			return err
		},
		"DescriptorSource": func() error {
			_, err := generate.DescriptorSource("generated", missing, children)
			return err
		},
		"DescriptorTestSource": func() error {
			_, err := generate.DescriptorTestSource("generated", missing, children)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorContains(t, call(), "targets missing table")
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
