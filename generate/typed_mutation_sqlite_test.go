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

func TestGeneratedMutationPlansRunAgainstSQLite(t *testing.T) {
	table := schema.TableDef{
		Name:       "items",
		PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}, Identity: schema.IdentityAlways},
			{Name: "required", Type: schema.TextType{}},
			{Name: "count", Type: schema.IntegerType{}, Default: "7"},
			{Name: "enabled", Type: schema.BooleanType{}, Default: "0"},
			{Name: "label", Type: schema.TextType{}, Nullable: true, GoBinding: &schema.GoBinding{Type: "NullableString", NullableType: "NullableString"}},
			{Name: "note", Type: schema.TextType{}, Nullable: true},
		},
	}
	generated, err := generate.PackageSource("generated", table)
	require.NoError(t, err)

	directory := t.TempDir()
	repository, err := filepath.Abs("..")
	require.NoError(t, err)
	module := "module example.com/generatedmutation\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte(module), 0o600))
	packageDir := filepath.Join(directory, "generated")
	require.NoError(t, os.MkdirAll(packageDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "generated.go"), generated, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "nullable.go"), []byte(nullableStringSource), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "mutation_test.go"), []byte(generatedMutationConsumerSource), 0o600))

	command := exec.CommandContext(t.Context(), "go", "mod", "tidy")
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "go mod tidy output:\n%s", output)
	command = exec.CommandContext(t.Context(), "go", "test", "./...")
	command.Dir = directory
	output, err = command.CombinedOutput()
	require.NoErrorf(t, err, "generated SQLite consumer output:\n%s", output)
}

func TestGeneratedMutationPlansRejectForbiddenCompileCallers(t *testing.T) {
	table := schema.TableDef{
		Name:       "items",
		PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}, Identity: schema.IdentityAlways},
			{Name: "required", Type: schema.TextType{}},
			{Name: "optional", Type: schema.TextType{}, Nullable: true},
			{Name: "computed", Type: schema.TextType{}, GeneratedExpression: "lower(required)", GeneratedStorage: schema.GeneratedVirtual},
		},
	}
	generated, err := generate.PackageSource("generated", table)
	require.NoError(t, err)
	typeChanged := table.Clone()
	typeChanged.Columns[1].Type = schema.IntegerType{}
	typeChangedSource, err := generate.PackageSource("generated", typeChanged)
	require.NoError(t, err)
	repository, err := filepath.Abs("..")
	require.NoError(t, err)
	cases := []struct {
		name   string
		call   string
		want   string
		source []byte
	}{
		{"wrong setter type", `generated.NewItemsCreate().Required(42)`, "cannot use", generated},
		{"nonnull clear", `generated.NewItemsCreate().ClearRequired()`, "ClearRequired", generated},
		{"identity setter", `generated.NewItemsCreate().ID(1)`, "ID", generated},
		{"generated setter", `generated.NewItemsCreate().Computed("x")`, "Computed", generated},
		{"primary key patch setter", `generated.NewItemsPatch().ID(1)`, "ID", generated},
		{"stale renamed caller", `generated.NewItemsCreate().Name("x")`, "Name", generated},
		{"stale type caller", `generated.NewItemsCreate().Required("x")`, "cannot use", typeChangedSource},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			module := "module example.com/invalidmutation\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
			require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte(module), 0o600))
			packageDir := filepath.Join(directory, "generated")
			require.NoError(t, os.MkdirAll(packageDir, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(packageDir, "generated.go"), test.source, 0o600))
			consumer := "package invalidmutation_test\n\nimport (\n\t\"testing\"\n\t\"example.com/invalidmutation/generated\"\n)\n\nfunc TestInvalid(t *testing.T) { _ = " + test.call + " }\n"
			require.NoError(t, os.WriteFile(filepath.Join(directory, "invalid_test.go"), []byte(consumer), 0o600))
			command := exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "-run", "^$", "./...")
			command.Dir = directory
			output, err := command.CombinedOutput()
			require.Error(t, err, "%s unexpectedly compiled:\n%s", test.name, output)
			require.Contains(t, string(output), test.want)
		})
	}
}

const nullableStringSource = `package generated

import (
	"database/sql/driver"
	"fmt"
)

type NullableString struct {
	Text string
	Valid bool
}

func (s *NullableString) Scan(value any) error {
	if value == nil {
		s.Text, s.Valid = "", false
		return nil
	}
	s.Valid = true
	switch value := value.(type) {
	case string:
		s.Text = value
	case []byte:
		s.Text = string(value)
	default:
		return fmt.Errorf("generated.NullableString: cannot scan %T", value)
	}
	return nil
}

func (s NullableString) Value() (driver.Value, error) {
	if !s.Valid {
		return nil, nil
	}
	return s.Text, nil
}
`

const generatedMutationConsumerSource = `package generatedmutation_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"example.com/generatedmutation/generated"
	_ "modernc.org/sqlite"
)

func TestGeneratedCreateAndPatchMatrix(t *testing.T) {
	ctx := context.Background()
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil { t.Fatal(err) }
	defer func() { _ = sqlDB.Close() }()
	sqlDB.SetMaxOpenConns(1)
	db, err := rasql.New(sqlDB, dialect.SQLite())
	if err != nil { t.Fatal(err) }
	if _, err := sqlDB.ExecContext(ctx, "CREATE TABLE items (\n\t\tid INTEGER PRIMARY KEY AUTOINCREMENT,\n\t\trequired TEXT NOT NULL,\n\t\tcount INTEGER NOT NULL DEFAULT 7,\n\t\tenabled INTEGER NOT NULL DEFAULT 0,\n\t\tlabel TEXT,\n\t\tnote TEXT\n\t)"); err != nil { t.Fatal(err) }

	// Each row uses the generated builder, including identity and default behavior.
	omitted, err := rasql.QueryCreate(ctx, db, generated.NewItemsCreate().Required("omitted").Plan())
	if err != nil || omitted.ID == 0 || omitted.Count != 7 || omitted.Enabled { t.Fatalf("omitted create = %#v, %v", omitted, err) }
	zero, err := rasql.QueryCreate(ctx, db, generated.NewItemsCreate().Required("zero").Count(0).Enabled(false).Plan())
	if err != nil || zero.Count != 0 || zero.Enabled { t.Fatalf("zero create = %#v, %v", zero, err) }
	value, err := rasql.QueryCreate(ctx, db, generated.NewItemsCreate().Required("value").Count(4).Enabled(true).Label(generated.NullableString{Text: "label", Valid: true}).Note(pointer("note")).Plan())
	if err != nil || !value.Enabled || value.Label.Text != "label" || !value.Label.Valid || value.Note == nil || *value.Note != "note" { t.Fatalf("value create = %#v, %v", value, err) }
	null, err := rasql.QueryCreate(ctx, db, generated.NewItemsCreate().Required("null").ClearLabel().ClearNote().Plan())
	if err != nil || null.Label.Valid || null.Note != nil { t.Fatalf("null create = %#v, %v", null, err) }
	defaults, err := rasql.QueryCreate(ctx, db, generated.NewItemsCreate().Required("defaults").DefaultCount().DefaultEnabled().Plan())
	if err != nil || defaults.Count != 7 || defaults.Enabled { t.Fatalf("default create = %#v, %v", defaults, err) }

	// Distinct patch calls preserve omission, false, zero, and NULL semantics.
	patched, err := rasql.QueryPatchOne(ctx, db, patchPlan(t, generated.NewItemsPatch().Required("omission-patch"), query.EqualValue(generated.Items().ID(), omitted.ID)))
	if err != nil || patched.Count != 7 || patched.Enabled { t.Fatalf("omission patch = %#v, %v", patched, err) }
	patched, err = rasql.QueryPatchOne(ctx, db, patchPlan(t, generated.NewItemsPatch().Enabled(false), query.EqualValue(generated.Items().ID(), value.ID)))
	if err != nil || patched.Enabled { t.Fatalf("false patch = %#v, %v", patched, err) }
	patched, err = rasql.QueryPatchOne(ctx, db, patchPlan(t, generated.NewItemsPatch().Count(0), query.EqualValue(generated.Items().ID(), zero.ID)))
	if err != nil || patched.Count != 0 { t.Fatalf("zero patch = %#v, %v", patched, err) }
	patched, err = rasql.QueryPatchOne(ctx, db, patchPlan(t, generated.NewItemsPatch().ClearLabel().ClearNote(), query.EqualValue(generated.Items().ID(), value.ID)))
	if err != nil || patched.Label.Valid || patched.Note != nil { t.Fatalf("NULL patch = %#v, %v", patched, err) }

	// Derived builders leave their base unchanged, and duplicate setters remain sticky errors.
	base := generated.NewItemsCreate().Required("base")
	left, right := base.Count(1), base.Count(2)
	baseRow, err := rasql.QueryCreate(ctx, db, base.Plan())
	if err != nil || baseRow.Count != 7 { t.Fatalf("base changed: %#v, %v", baseRow, err) }
	leftRow, err := rasql.QueryCreate(ctx, db, left.Plan())
	if err != nil || leftRow.Count != 1 { t.Fatalf("left variant: %#v, %v", leftRow, err) }
	rightRow, err := rasql.QueryCreate(ctx, db, right.Plan())
	if err != nil || rightRow.Count != 2 { t.Fatalf("right variant: %#v, %v", rightRow, err) }
	if _, err := rasql.ExecCreate(ctx, db, generated.NewItemsCreate().Required("duplicate").Required("again").Plan()); err == nil { t.Fatal("duplicate setter unexpectedly succeeded") }

	rows, err := rasql.QueryPatchAll(ctx, db, patchPlan(t, generated.NewItemsPatch().Enabled(true), query.GreaterValue(generated.Items().ID(), int64(0))))
	if err != nil || len(rows) < 6 { t.Fatalf("patch all = %d rows, %v", len(rows), err) }
	if _, err := rasql.QueryPatchOne(ctx, db, patchPlan(t, generated.NewItemsPatch().Count(1), query.EqualValue(generated.Items().ID(), int64(-1)))); err != rasql.ErrNoRows { t.Fatalf("zero QueryPatchOne error = %v", err) }
	if _, err := rasql.QueryPatchOne(ctx, db, patchPlan(t, generated.NewItemsPatch().Count(1), query.GreaterValue(generated.Items().ID(), int64(0)))); err != rasql.ErrMultipleRows { t.Fatalf("multiple QueryPatchOne error = %v", err) }
}

func patchPlan(t *testing.T, builder generated.ItemsPatch, predicate query.Predicate) rasql.PatchPlan[generated.ItemsRow] {
	plan, err := builder.Where(predicate)
	if err != nil { t.Fatal(err) }
	return plan
}

func pointer(value string) *string { return &value }
`
