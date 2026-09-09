package generate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
			{Name: "label", Type: schema.TextType{}, Nullable: true},
			{Name: "note", Type: schema.TextType{}, Nullable: true},
		},
	}

	directory := t.TempDir()
	repository, err := filepath.Abs("..")
	require.NoError(t, err)
	module := "module example.com/generatedmutation\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte(module), 0o600))
	packageDir := filepath.Join(directory, "generated")
	require.NoError(t, compactPackageStore(t, packageDir, table).Write())
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
			{Name: "label", Type: schema.TextType{}, Nullable: true},
			{Name: "note", Type: schema.TextType{}, Nullable: true},
			{Name: "computed", Type: schema.TextType{}, GeneratedExpression: "lower(required)", GeneratedStorage: schema.GeneratedVirtual},
		},
	}
	typeChanged := table.Clone()
	typeChanged.Columns[1].Type = schema.IntegerType{}
	repository, err := filepath.Abs("..")
	require.NoError(t, err)
	cases := []struct {
		name  string
		call  string
		want  string
		table schema.TableDef
	}{
		{"wrong setter type", `generated.NewItemsCreate().Required(42)`, "cannot use", table},
		{"wrong nullable setter type", `generated.NewItemsCreate().Label(42)`, "cannot use", table},
		{"nonnull clear", `generated.NewItemsCreate().ClearRequired()`, "ClearRequired", table},
		{"identity setter", `generated.NewItemsCreate().ID(1)`, "ID", table},
		{"generated setter", `generated.NewItemsCreate().Computed("x")`, "Computed", table},
		{"primary key patch setter", `generated.NewItemsPatch().ID(1)`, "ID", table},
		{"stale renamed caller", `generated.NewItemsCreate().Name("x")`, "Name", table},
		{"stale type caller", `generated.NewItemsCreate().Required("x")`, "cannot use", typeChanged},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			module := "module example.com/invalidmutation\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
			require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte(module), 0o600))
			packageDir := filepath.Join(directory, "generated")
			require.NoError(t, compactPackageStore(t, packageDir, test.table).Write())
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

func TestGeneratedMutationCallerBecomesStaleAfterColumnRename(t *testing.T) {
	oldTable := schema.TableDef{
		Name:       "items",
		PrimaryKey: []string{"id"},
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "required", Type: schema.TextType{}}},
	}
	newTable := oldTable.Clone()
	newTable.Columns[1].Name = "renamed_required"
	repository, err := filepath.Abs("..")
	require.NoError(t, err)
	directory := t.TempDir()
	module := "module example.com/renamedmutation\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte(module), 0o600))
	packageDir := filepath.Join(directory, "generated")
	caller := "package renamedmutation_test\n\nimport (\n\t\"testing\"\n\t\"example.com/renamedmutation/generated\"\n)\n\nfunc TestOldCaller(t *testing.T) { _ = generated.NewItemsCreate().Required(\"old\") }\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, "caller_test.go"), []byte(caller), 0o600))
	require.NoError(t, compactPackageStore(t, packageDir, oldTable).Write())
	command := exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "-run", "^$", "./...")
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "old generated caller output:\n%s", output)
	require.NoError(t, compactPackageStore(t, packageDir, newTable).Write())
	command = exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "-run", "^$", "./...")
	command.Dir = directory
	output, err = command.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(output), "Required")
	corrected := strings.Replace(caller, ".Required(\"old\")", ".RenamedRequired(\"new\")", 1)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "caller_test.go"), []byte(corrected), 0o600))
	command = exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "-run", "^$", "./...")
	command.Dir = directory
	output, err = command.CombinedOutput()
	require.NoErrorf(t, err, "renamed generated caller output:\n%s", output)
}

const generatedMutationConsumerSource = `package generatedmutation_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"example.com/generatedmutation/generated"
	_ "modernc.org/sqlite"
)

func TestGeneratedCreateAndPatchMatrix(t *testing.T) {
	ctx := context.Background()
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil { t.Fatal(err) }
	defer func() { _ = sqlDB.Close() }()
	sqlDB.SetMaxOpenConns(1)
	if _, err := sqlDB.ExecContext(ctx, "CREATE TABLE items (\n\t\tid INTEGER PRIMARY KEY AUTOINCREMENT,\n\t\trequired TEXT NOT NULL,\n\t\tcount INTEGER NOT NULL DEFAULT 7,\n\t\tenabled INTEGER NOT NULL DEFAULT 0,\n\t\tlabel TEXT,\n\t\tnote TEXT\n\t)"); err != nil { t.Fatal(err) }
	raw, err := rasql.New(sqlDB, dialect.SQLite())
	if err != nil { t.Fatal(err) }
	profile, err := rasql.DiscoverEngineProfile(ctx, raw, "sqlite-3.35")
	if err != nil { t.Fatal(err) }
	executor, err := rasql.AsExecutor(raw, profile)
	if err != nil { t.Fatal(err) }

	source, err := generated.Items().Source("")
	if err != nil { t.Fatal(err) }
	columns, err := (generated.ItemsColumns{}).Bind(source)
	if err != nil { t.Fatal(err) }
	projection, err := generated.ItemsProjection(columns)
	if err != nil { t.Fatal(err) }
	returning := func(plan rasql.MutationPlan) generated.ItemsRow {
		t.Helper()
		q, err := rasql.Returning(plan, projection)
		if err != nil { t.Fatal(err) }
		row, err := rasql.One(ctx, executor, q)
		if err != nil { t.Fatal(err) }
		return row
	}
	patchOn := func(builder generated.ItemsPatch, id int64) generated.ItemsRow {
		t.Helper()
		plan, err := builder.Where(rasql.EqualValue(columns.ID.Expr(), id))
		if err != nil { t.Fatal(err) }
		return returning(plan)
	}

	// Each row uses the generated builder, including identity and default behavior.
	omittedPlan, err := generated.NewItemsCreate().Required("omitted").Plan()
	if err != nil { t.Fatal(err) }
	omitted := returning(omittedPlan)
	if omitted.ID == 0 || omitted.Count != 7 || omitted.Enabled { t.Fatalf("omitted create = %#v", omitted) }
	zeroPlan, err := generated.NewItemsCreate().Required("zero").Count(0).Enabled(false).Plan()
	if err != nil { t.Fatal(err) }
	zero := returning(zeroPlan)
	if zero.Count != 0 || zero.Enabled { t.Fatalf("zero create = %#v", zero) }
	valuePlan, err := generated.NewItemsCreate().Required("value").Count(4).Enabled(true).Label("label").Note("note").Plan()
	if err != nil { t.Fatal(err) }
	value := returning(valuePlan)
	if !value.Enabled || value.Label.Value != "label" || !value.Label.Valid || !value.Note.Valid || value.Note.Value != "note" { t.Fatalf("value create = %#v", value) }
	nullPlan, err := generated.NewItemsCreate().Required("null").ClearLabel().ClearNote().Plan()
	if err != nil { t.Fatal(err) }
	null := returning(nullPlan)
	if null.Label.Valid || null.Note.Valid { t.Fatalf("null create = %#v", null) }
	defaultsPlan, err := generated.NewItemsCreate().Required("defaults").DefaultCount().DefaultEnabled().Plan()
	if err != nil { t.Fatal(err) }
	defaults := returning(defaultsPlan)
	if defaults.Count != 7 || defaults.Enabled { t.Fatalf("default create = %#v", defaults) }

	// Distinct patch calls preserve omission, false, zero, and NULL semantics.
	patched := patchOn(generated.NewItemsPatch().Required("omission-patch"), omitted.ID)
	if patched.Count != 7 || patched.Enabled { t.Fatalf("omission patch = %#v", patched) }
	patched = patchOn(generated.NewItemsPatch().Enabled(false), value.ID)
	if patched.Enabled { t.Fatalf("false patch = %#v", patched) }
	patched = patchOn(generated.NewItemsPatch().Count(0), zero.ID)
	if patched.Count != 0 { t.Fatalf("zero patch = %#v", patched) }
	patched = patchOn(generated.NewItemsPatch().ClearLabel().ClearNote(), value.ID)
	if patched.Label.Valid || patched.Note.Valid { t.Fatalf("NULL patch = %#v", patched) }

	// Derived builders leave their base unchanged, and duplicate setters remain sticky errors.
	base := generated.NewItemsCreate().Required("base")
	left, right := base.Count(1), base.Count(2)
	basePlan, err := base.Plan()
	if err != nil { t.Fatal(err) }
	baseRow := returning(basePlan)
	if baseRow.Count != 7 { t.Fatalf("base changed: %#v", baseRow) }
	leftPlan, err := left.Plan()
	if err != nil { t.Fatal(err) }
	leftRow := returning(leftPlan)
	if leftRow.Count != 1 { t.Fatalf("left variant: %#v", leftRow) }
	rightPlan, err := right.Plan()
	if err != nil { t.Fatal(err) }
	rightRow := returning(rightPlan)
	if rightRow.Count != 2 { t.Fatalf("right variant: %#v", rightRow) }
	if _, err := generated.NewItemsCreate().Required("duplicate").Required("again").Plan(); err == nil { t.Fatal("duplicate setter unexpectedly succeeded") }

	patchBase := generated.NewItemsPatch().Required("patch-base")
	patchLeft, patchRight := patchBase.Count(1), patchBase.Count(2)
	patchBaseRow := patchOn(patchBase, baseRow.ID)
	if patchBaseRow.Count != 7 { t.Fatalf("patch base changed: %#v", patchBaseRow) }
	patchLeftRow := patchOn(patchLeft, leftRow.ID)
	if patchLeftRow.Count != 1 { t.Fatalf("patch left variant: %#v", patchLeftRow) }
	patchRightRow := patchOn(patchRight, rightRow.ID)
	if patchRightRow.Count != 2 { t.Fatalf("patch right variant: %#v", patchRightRow) }

	missingPlan, err := generated.NewItemsPatch().Count(1).Where(rasql.EqualValue(columns.ID.Expr(), int64(-1)))
	if err != nil { t.Fatal(err) }
	missingQuery, err := rasql.Returning(missingPlan, projection)
	if err != nil { t.Fatal(err) }
	if _, err := rasql.One(ctx, executor, missingQuery); !errors.Is(err, rasql.ErrNoRows) { t.Fatalf("missing patch error = %v", err) }
}
`
