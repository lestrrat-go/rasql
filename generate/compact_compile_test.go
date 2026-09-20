package generate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/scratchmod"
	"github.com/stretchr/testify/require"
)

func TestCompactGeneratedNegativeCallersFailAtTheirTarget(t *testing.T) {
	root := renderCompactCompileModule(t, richCompactInput(t))
	compileFail := func(name, source, want string) {
		t.Helper()
		dir := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0o600))
		command := exec.Command("go", "test", "-mod=mod", "./"+name)
		command.Dir = root
		output, err := command.CombinedOutput()
		require.Error(t, err, "%s unexpectedly compiled:\n%s", name, output)
		require.Contains(t, string(output), want, "%s failed for an unrelated reason:\n%s", name, output)
	}

	// A column is a field of the generated table, not a method on it, so
	// calling one is a compile error naming the field's type rather than an
	// undefined selector.
	compileFail("removed_accessor", `package main

import generated "example.com/compactcompile/generated"

func main() { _ = generated.Account().ID() }
`, "is not a function")
	compileFail("nonnullable_clear", `package main

import generated "example.com/compactcompile/generated"

func main() { _ = generated.Project().Create().ClearTitle() }
`, "ClearTitle undefined")
	compileFail("no_default_default", `package main

import generated "example.com/compactcompile/generated"

func main() { _ = generated.Account().Create().DefaultNickname() }
`, "DefaultNickname undefined")
	compileFail("wrong_setter_type", `package main

import generated "example.com/compactcompile/generated"

func main() { _ = generated.Project().Create().Title(42) }
`, "cannot use 42")
	compileFail("view_write", `package main

import generated "example.com/compactcompile/generated"

func main() { _ = generated.ActiveUser().Create() }
`, "Create undefined")

	generatedRoot := renderCompactCompileModule(t, richCompactInputWithGeneratedColumn(t))
	dir := filepath.Join(generatedRoot, "generated_column_write")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main

import generated "example.com/compactcompile/generated"

func main() { _ = generated.Account().Create().ComputedID(42) }
`), 0o600))
	command := exec.Command("go", "test", "-mod=mod", "./generated_column_write")
	command.Dir = generatedRoot
	output, err := command.CombinedOutput()
	require.Error(t, err, "generated column setter unexpectedly compiled:\n%s", output)
	require.Contains(t, string(output), "ComputedID undefined", "generated column failed for an unrelated reason:\n%s", output)
}

func renderCompactCompileModule(t *testing.T, input generate.EmitterInput) string {
	t.Helper()
	root := t.TempDir()
	store, err := generate.RenderCompact(input)
	require.NoError(t, err)
	store.Root, store.Dir = root, "generated"
	plan, err := store.Plan()
	require.NoError(t, err)
	require.NoError(t, plan.Commit())
	file, err := scratchmod.ForModule(repoRoot(t), "example.com/compactcompile")
	require.NoError(t, err)
	require.NoError(t, file.AddRequire("example.com/domain", "v1.0.0"))
	require.NoError(t, file.AddReplace("example.com/domain", "", "./domain", ""))
	data, err := scratchmod.Format(file)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), data, 0o600))
	require.NoError(t, scratchmod.WriteGoSum(root, repoRoot(t)))
	domain := filepath.Join(root, "domain")
	require.NoError(t, os.MkdirAll(domain, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(domain, "go.mod"), []byte("module example.com/domain\n\ngo 1.26\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(domain, "money.go"), []byte(richDomainSource), 0o600))
	command := exec.Command("go", "test", "-mod=mod", "./generated")
	command.Dir = root
	output, err := command.CombinedOutput()
	require.NoError(t, err, "generated module did not compile:\n%s", output)
	return root
}

func richCompactInputWithGeneratedColumn(t *testing.T) generate.EmitterInput {
	t.Helper()
	catalog, relations, config := richCompactParts(t)
	for index := range catalog.Objects {
		if catalog.Objects[index].ID != "users" {
			continue
		}
		catalog.Objects[index].Columns = append(catalog.Objects[index].Columns, compilerir.PhysicalColumn{
			Name: "computed_id", Ordinal: 3, LogicalKind: "integer", GeneratedSQL: "id + 1", GeneratedStorage: "STORED",
		})
	}
	result, err := generate.NewEmitterInput(catalog, relations, config)
	require.NoError(t, err)
	return result
}

// TestCompactAwkwardColumnNamesCompile builds a package whose table holds every
// shape of awkward column name at once and compiles a caller against it. The
// unit tests beside this one check what the generator kept; this one checks the
// thing that decides whether any of it was worth doing, which is that a caller
// can read and write every one of those columns.
func TestCompactAwkwardColumnNamesCompile(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{
		Engine: compilerir.EngineIdentity{Dialect: "sqlite", Version: "3"},
		Objects: []compilerir.PhysicalObject{{
			ID: "widgets", Kind: "table", Name: "widgets",
			Columns: []compilerir.PhysicalColumn{
				{Name: "id", LogicalKind: "integer"},
				{Name: "ref", Ordinal: 1, LogicalKind: "text"},
				{Name: "as", Ordinal: 2, LogicalKind: "text"},
				{Name: "create", Ordinal: 3, LogicalKind: "text"},
				{Name: "scan_row", Ordinal: 4, LogicalKind: "text"},
				{Name: "plan", Ordinal: 5, LogicalKind: "text"},
				{Name: "where", Ordinal: 6, LogicalKind: "text"},
				{Name: "user_id", Ordinal: 7, LogicalKind: "integer"},
				{Name: "user__id", Ordinal: 8, LogicalKind: "integer"},
			},
			Constraints: []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}},
		}},
	}
	config := compilerir.GoConfig{Package: "store", Output: "generated", Emitter: "compact", Objects: []compilerir.ObjectGoName{{ID: "widgets", File: "widgets_gen.go"}}}
	input, err := generate.NewEmitterInput(catalog, compilerir.MappingConfig{}, config)
	require.NoError(t, err)

	root := t.TempDir()
	store, err := generate.RenderCompact(input)
	require.NoError(t, err)
	store.Root, store.Dir = root, "generated"
	plan, err := store.Plan()
	require.NoError(t, err)
	require.NoError(t, plan.Commit())

	file, err := scratchmod.ForModule(repoRoot(t), "example.com/awkward")
	require.NoError(t, err)
	data, err := scratchmod.Format(file)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), data, 0o600))
	require.NoError(t, scratchmod.WriteGoSum(root, repoRoot(t)))

	caller := `package main

import (
	"context"

	"github.com/lestrrat-go/rasql"
	generated "example.com/awkward/generated"
)

func read(ctx context.Context, db rasql.Executor) error {
	widgets := generated.Widgets()
	projection, err := generated.WidgetsProjection(widgets)
	if err != nil {
		return err
	}
	rows, err := rasql.All(ctx, db, rasql.Select(widgets, projection))
	if err != nil {
		return err
	}
	// Every awkward column comes back in the row, including the one named
	// after the row type's own scan method.
	for _, row := range rows {
		_, _, _, _, _, _ = row.Ref, row.As, row.Create, row.ScanRow, row.Plan, row.Where
	}
	return nil
}

func write(ctx context.Context, db rasql.Executor) error {
	widgets := generated.Widgets()

	// A column named like one of the table's own methods has an ordinary
	// setter; naming the embedded struct reaches its column for a predicate.
	_, err := widgets.Create().
		Ref("r").
		As("a").
		Create("c").
		ScanRow("s").
		Exec(ctx, db)
	if err != nil {
		return err
	}

	// A column named like a builder terminal has no setter, so the column
	// field on the table goes to rasql.SetField, which is what the setter
	// would have called anyway.
	create, err := rasql.NewCreatePlan(generated.WidgetsHandle(widgets),
		rasql.SetField(widgets.WidgetsExpressions.Plan, "p"),
		rasql.SetField(widgets.WidgetsExpressions.Where, "w"),
	)
	if err != nil {
		return err
	}
	if _, err := rasql.Exec(ctx, db, create); err != nil {
		return err
	}

	patch, err := rasql.NewPatchPlan(generated.WidgetsHandle(widgets),
		rasql.EqualValue(widgets.WidgetsExpressions.Ref.Expr(), "r"),
		rasql.SetField(widgets.WidgetsExpressions.Plan, "p2"),
	)
	if err != nil {
		return err
	}
	_, err = rasql.Exec(ctx, db, patch)
	return err
}

func leftOut() error {
	// user__id has no Go name of its own, because user_id took it. The
	// descriptor still declares it, so it is named by string.
	return generated.Widgets().Ref().Column("user__id").Validate()
}

func main() {
	_, _, _ = read, write, leftOut
}
`
	dir := filepath.Join(root, "caller")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte(caller), 0o600))
	command := exec.Command("go", "build", "-mod=mod", "-buildvcs=false", "./...")
	command.Dir = root
	output, err := command.CombinedOutput()
	require.NoError(t, err, "the package and its caller must compile:\n%s", output)
}
