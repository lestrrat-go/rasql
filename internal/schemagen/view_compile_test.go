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
// view's wrapper has no Create, Patch or Delete method, while a generated
// ordinary table's wrapper has all three. There is no longer any way to reach
// a raw rasql.Table[T] from outside the generated package -- the wrapper's own
// Table method is gone -- so this compile failure is now the only way a
// caller could try one of the three against a view; nothing at runtime
// remains to refuse.
//
// CreateTable is not among them. It takes a rasql.CatalogObject, which both
// wrappers satisfy, so a view reaches it and is refused when the descriptor's
// OperationDDL bit is read. TestGeneratedViewHandleIsRefusedWhenThePlanIsBuilt
// pins that refusal.
//
// The legacy version of this test used the root package's Insert, Update and
// DeleteFrom convenience functions, all removed since (commit "remove
// superseded ORM facades") as part of an unrelated, still in-flight API
// cleanup. The generated wrapper's own Create, Patch and Delete methods are
// their current replacements and prove the same rejection.
func TestGeneratedViewRejectsEachMutationIndependently(t *testing.T) {
	for name, proof := range map[string]struct{ view, table string }{
		"insert": {
			view:  `_, _ = generated.ActiveUsers().Create().Plan()`,
			table: `_, _ = generated.Users().Create().Plan()`,
		},
		"update": {
			view:  `_, _ = generated.ActiveUsers().Patch().Where(rasql.Predicate{})`,
			table: `_, _ = generated.Users().Patch().Where(rasql.Predicate{})`,
		},
		"delete": {
			view:  `_, _ = generated.ActiveUsers().Delete(rasql.Predicate{})`,
			table: `_, _ = generated.Users().Delete(rasql.Predicate{})`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			header := "package generated_test\n\nimport (\n\t\"github.com/lestrrat-go/rasql\"\n\t\"example.com/generated/generated\"\n)\n\nvar _ rasql.Predicate\n\n"

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
// failures above do not: DDL. There is no longer any way to reach
// NewCreatePlan, NewPatchPlan or NewDeletePlan for a view from outside the
// generated package at all, since the wrapper's Table method is gone and
// those three take a plain rasql.Table[T] no exported call now produces --
// TestGeneratedViewRejectsEachMutationIndependently is the whole story for
// insert, update and delete. CreateTable is different: it takes the wrapper
// itself, so a view reaches it and is refused at runtime when
// OperationDDL is read off the descriptor, naming the object and the
// operation.
func TestGeneratedViewHandleIsRefusedWhenThePlanIsBuilt(t *testing.T) {
	body := "package generated_test\n\n" +
		"import (\n" +
		"\t\"context\"\n\t\"strings\"\n\t\"testing\"\n\n" +
		"\t\"github.com/lestrrat-go/rasql\"\n\t\"example.com/generated/generated\"\n" +
		")\n\n" +
		"func TestRefused(t *testing.T) {\n" +
		"\tctx := context.Background()\n" +
		"\tvar db rasql.DB\n" +
		"\terr := rasql.CreateTable(ctx, db, generated.ActiveUsers())\n" +
		"\tif err == nil {\n\t\tt.Fatal(\"ddl was accepted\")\n\t}\n" +
		"\tif !strings.Contains(err.Error(), `\"active_users\"`) {\n\t\tt.Fatalf(\"ddl did not name the object: %s\", err)\n\t}\n" +
		"}\n"
	directory := writeViewScratchModule(t, body)
	output, err := runViewScratchModule(t, directory)
	require.NoError(t, err, "generated view consumer failed:\n%s", output)
}
