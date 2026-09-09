package generate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/internal/scratchmod"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestCompactHonoursColumnGoBinding proves a column's GoBinding reaches the
// compact emitter end to end: the row struct's field uses the bound type,
// the generated file imports the bound type's package, and -- the part a
// string check alone cannot prove -- the generated package actually
// compiles against a real definition of that type.
func TestCompactHonoursColumnGoBinding(t *testing.T) {
	accounts := schema.TableDef{
		Name: "accounts", PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "balance", Type: schema.IntegerType{}, GoBinding: &schema.GoBinding{
				Type:    "domain.Money",
				Imports: []schema.GoImport{{Path: "example.com/domain", Name: "domain"}},
			}},
			{Name: "limit", Type: schema.IntegerType{}, Nullable: true, GoBinding: &schema.GoBinding{
				Type:    "domain.Money",
				Imports: []schema.GoImport{{Path: "example.com/domain", Name: "domain"}},
			}},
		},
	}

	engine := compilerir.EngineIdentity{Dialect: "sqlite", Version: "3"}
	catalog, diagnostics := compilerir.PhysicalFromTableDefs(engine, []schema.TableDef{accounts})
	require.Empty(t, diagnostics)
	catalog, diagnostics = compilerir.AssignObjectIDs(catalog, compilerir.IdentityInput{SourceIdentity: "column-go-binding-fixture"})
	require.Empty(t, diagnostics)
	semantic, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{}, nil)
	require.Empty(t, diagnostics)

	require.Len(t, catalog.Objects, 1)
	object := catalog.Objects[0]
	columnBindings, err := schemagen.TableColumnGoBindings(object.ID, accounts, schemagen.BindingSetOptions{})
	require.NoError(t, err)
	require.Len(t, columnBindings, 2, "both balance and limit carry a GoBinding")

	config := compilerir.GoConfig{
		Package: "store", Output: "generated", Emitter: "compact",
		Objects:        []compilerir.ObjectGoName{{ID: object.ID, File: "accounts_gen.go"}},
		ColumnBindings: columnBindings,
	}
	model, diagnostics := compilerir.BuildGo(semantic, config)
	require.Empty(t, diagnostics)

	in, err := generate.NewEmitterInput(catalog, semantic, model, config, compilerir.MappingConfig{})
	require.NoError(t, err)
	store, err := generate.RenderCompact(in)
	require.NoError(t, err)

	root := t.TempDir()
	store.Root, store.Dir = root, "generated"
	plan, err := store.Plan()
	require.NoError(t, err)
	var accountsSource string
	for _, file := range plan.Files() {
		if filepath.Base(file.Path) == "accounts_gen.go" {
			accountsSource = string(file.Source)
		}
	}
	require.NotEmpty(t, accountsSource, "accounts_gen.go must be one of the rendered files")
	require.Contains(t, accountsSource, "type AccountsRow struct {\n\tID      int64\n\tBalance domain.Money\n\tLimit   *domain.Money\n}",
		"a nullable column with no NullableType defaults to a pointer, not rasql.Nullable[T]")
	require.Contains(t, accountsSource, `"example.com/domain"`, "the bound type's package must be imported")
	require.NoError(t, plan.Commit())

	file, err := scratchmod.ForModule(repoRoot(t), "example.com/columnbinding")
	require.NoError(t, err)
	require.NoError(t, file.AddRequire("example.com/domain", "v1.0.0"))
	require.NoError(t, file.AddReplace("example.com/domain", "", "./domain", ""))
	data, err := scratchmod.Format(file)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), data, 0o600))
	require.NoError(t, scratchmod.WriteGoSum(root, repoRoot(t)))
	domainDir := filepath.Join(root, "domain")
	require.NoError(t, os.MkdirAll(domainDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(domainDir, "go.mod"), []byte("module example.com/domain\n\ngo 1.26\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(domainDir, "money.go"), []byte(columnBindingDomainSource), 0o600))

	command := exec.Command("go", "build", "-mod=mod", "./generated")
	command.Dir = root
	// GOCACHE is deliberately not overridden here: see the matching comment
	// in cli/rasqlgen/compact_workflow_acceptance_test.go's
	// runCompactConsumer.
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	require.NoError(t, err, "generated package with a bound column did not compile:\n%s", output)
}

const columnBindingDomainSource = `package domain

type Money int64
`
