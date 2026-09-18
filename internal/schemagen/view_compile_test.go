package schemagen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lestrrat-go/rasql/internal/scratchmod"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func viewCompileFixture() (schema.TableDef, schema.TableDef) {
	users := schema.TableDef{
		Name: "users", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	}
	activeUsers := schema.TableDef{
		Name: "active_users", Kind: schema.ObjectView, Operations: schema.OperationRead,
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	}
	return users, activeUsers
}

// writeViewScratchModule generates a store holding one table and one view into
// a scratch module, writes body as its only test file, and returns the
// directory for a `go test` run.
func writeViewScratchModule(t *testing.T, body string) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repository := filepath.Clean(filepath.Join(filepath.Dir(filename), "../.."))
	users, activeUsers := viewCompileFixture()
	directory := t.TempDir()
	require.NoError(t, compactStore(t, filepath.Join(directory, "generated"), users, activeUsers).Write())
	require.NoError(t, os.WriteFile(filepath.Join(directory, "usage_test.go"), []byte(body), 0o600))
	require.NoError(t, scratchmod.Write(directory, repository, "example.com/generated"))
	return directory
}

func runViewScratchModule(t *testing.T, directory string) ([]byte, error) {
	t.Helper()

	command := exec.Command("go", "test", "./...")
	command.Dir = directory
	return command.CombinedOutput()
}

// TestGeneratedViewRejectsEachMutationIndependently proves that a generated
// view's table type cannot be handed to any entry point that writes rows, and
// that a generated ordinary table's handle can. The wrapper is a struct of its
// own, so it is not a rasql.Table[T] and the call does not compile; the same
// call written against the table's handle does.
//
// CreateTable is not among them. It takes a rasql.CatalogObject, which both
// wrappers satisfy, so a view reaches it and is refused when the descriptor's
// OperationDDL bit is read. TestGeneratedViewHandleIsRefusedWhenThePlanIsBuilt
// pins that refusal.
//
// The legacy version of this test used the root package's Insert, Update and
// DeleteFrom convenience functions, all removed since (commit "remove
// superseded ORM facades") as part of an unrelated, still in-flight API
// cleanup. NewCreatePlan, NewPatchPlan, NewDeletePlan and CreateTable are
// their current replacements and prove the same rejection.
func TestGeneratedViewRejectsEachMutationIndependently(t *testing.T) {
	for name, proof := range map[string]struct{ view, table string }{
		"insert": {
			view:  `_, _ = rasql.NewCreatePlan(generated.ActiveUsers())`,
			table: `_, _ = rasql.NewCreatePlan(generated.Users().Table())`,
		},
		"update": {
			view:  `_, _ = rasql.NewPatchPlan(generated.ActiveUsers(), rasql.Predicate{})`,
			table: `_, _ = rasql.NewPatchPlan(generated.Users().Table(), rasql.Predicate{})`,
		},
		"delete": {
			view:  `_, _ = rasql.NewDeletePlan(generated.ActiveUsers(), query.Predicate{})`,
			table: `_, _ = rasql.NewDeletePlan(generated.Users().Table(), query.Predicate{})`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			header := "package generated_test\n\nimport (\n\t\"context\"\n\t\"github.com/lestrrat-go/rasql\"\n\t\"github.com/lestrrat-go/rasql/query\"\n\t\"example.com/generated/generated\"\n)\n\nvar ctx = context.Background()\nvar db rasql.DB\nvar _ = query.Predicate{}\n"

			t.Run("the table compiles", func(t *testing.T) {
				directory := writeViewScratchModule(t, header+"func proof() {\n"+proof.table+"\n}\n")
				output, err := runViewScratchModule(t, directory)
				require.NoError(t, err, "table mutation unexpectedly failed:\n%s", output)
			})

			t.Run("the view does not", func(t *testing.T) {
				directory := writeViewScratchModule(t, header+"func proof() {\n"+proof.view+"\n}\n")
				output, err := runViewScratchModule(t, directory)
				require.Error(t, err, "mutation unexpectedly compiled:\n%s", output)
			})
		})
	}
}

// TestGeneratedViewHandleIsRefusedWhenThePlanIsBuilt covers what the compile
// error above does not. The generated wrapper's handle is an unexported
// embedded field, but its Table method still hands back the plain
// rasql.Table[T] it holds, so a caller can write
// generated.ActiveUsers().Table() and reach every mutation constructor, and
// CreateTable takes the wrapper itself; each entry point reads
// OperationInsert, OperationUpdate, OperationDelete or OperationDDL off the
// descriptor and refuses, naming the object and the operation. This is the
// check that stops a write to a view even when a caller reaches around the
// wrapper's own Create, Patch and Delete methods -- a view has none of the
// three, so this is also the only way to try one against it.
func TestGeneratedViewHandleIsRefusedWhenThePlanIsBuilt(t *testing.T) {
	body := "package generated_test\n\n" +
		"import (\n" +
		"\t\"context\"\n\t\"strings\"\n\t\"testing\"\n\n" +
		"\t\"github.com/lestrrat-go/rasql\"\n\t\"github.com/lestrrat-go/rasql/query\"\n\t\"example.com/generated/generated\"\n" +
		")\n\n" +
		"func TestRefused(t *testing.T) {\n" +
		"\tctx := context.Background()\n" +
		"\tvar db rasql.DB\n" +
		"\tview := generated.ActiveUsers().Table()\n" +
		"\tfor name, call := range map[string]func() error{\n" +
		"\t\t\"insert\": func() error { _, err := rasql.NewCreatePlan(view); return err },\n" +
		"\t\t\"update\": func() error { _, err := rasql.NewPatchPlan(view, rasql.Predicate{}); return err },\n" +
		"\t\t\"delete\": func() error { _, err := rasql.NewDeletePlan(view, query.Predicate{}); return err },\n" +
		"\t\t\"ddl\":    func() error { return rasql.CreateTable(ctx, db, generated.ActiveUsers()) },\n" +
		"\t} {\n" +
		"\t\terr := call()\n" +
		"\t\tif err == nil {\n\t\t\tt.Fatalf(\"%s was accepted\", name)\n\t\t}\n" +
		"\t\tif !strings.Contains(err.Error(), `\"active_users\"`) {\n\t\t\tt.Fatalf(\"%s did not name the object: %s\", name, err)\n\t\t}\n" +
		"\t}\n" +
		"}\n"
	directory := writeViewScratchModule(t, body)
	output, err := runViewScratchModule(t, directory)
	require.NoError(t, err, "generated view consumer failed:\n%s", output)
}
