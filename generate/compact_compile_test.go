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

	compileFail("removed_accessor", `package main

import generated "example.com/compactcompile/generated"

func main() { _ = generated.Account().ID() }
`, "ID undefined")
	compileFail("nonnullable_clear", `package main

import generated "example.com/compactcompile/generated"

func main() { _ = generated.NewProjectCreate().ClearTitle() }
`, "ClearTitle undefined")
	compileFail("no_default_default", `package main

import generated "example.com/compactcompile/generated"

func main() { _ = generated.NewAccountCreate().DefaultNickname() }
`, "DefaultNickname undefined")
	compileFail("wrong_setter_type", `package main

import generated "example.com/compactcompile/generated"

func main() { _ = generated.NewProjectCreate().Title(42) }
`, "cannot use 42")
	compileFail("view_write", `package main

import generated "example.com/compactcompile/generated"

func main() { _ = generated.NewActiveUserCreate() }
`, "undefined: generated.NewActiveUserCreate")

	generatedRoot := renderCompactCompileModule(t, richCompactInputWithGeneratedColumn(t))
	dir := filepath.Join(generatedRoot, "generated_column_write")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main

import generated "example.com/compactcompile/generated"

func main() { _ = generated.NewAccountCreate().ComputedID(42) }
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
	in := richCompactInput(t)
	catalog := in.Catalog.Clone()
	for index := range catalog.Objects {
		if catalog.Objects[index].ID != "users" {
			continue
		}
		catalog.Objects[index].Columns = append(catalog.Objects[index].Columns, compilerir.PhysicalColumn{
			Name: "computed_id", Ordinal: 3, LogicalKind: "integer", GeneratedSQL: "id + 1", GeneratedStorage: "STORED",
		})
	}
	semantic, diagnostics := compilerir.BuildSemantic(catalog, in.Mappings, nil)
	for _, diagnostic := range diagnostics {
		require.NotEqual(t, compilerir.DiagnosticError, diagnostic.Level, diagnostic.Message)
	}
	model, diagnostics := compilerir.BuildGo(semantic, in.Generation)
	for _, diagnostic := range diagnostics {
		require.NotEqual(t, compilerir.DiagnosticError, diagnostic.Level, diagnostic.Message)
	}
	result, err := generate.NewEmitterInput(catalog, semantic, model, in.Generation, in.Mappings)
	require.NoError(t, err)
	return result
}
