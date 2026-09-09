package schemagen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestGeneratedViewRejectsEachMutationIndependently proves that a
// generated view's table type, which embeds rasql.ReadTable rather than
// rasql.Table, cannot satisfy rasql.Table[T] and so is rejected at compile
// time by every entry point that requires a writable table: the mutation
// plan constructors and CreateTable. A generated ordinary table's type,
// which does embed rasql.Table, satisfies the same entry points and compiles.
//
// The legacy version of this test used the root package's Insert, Update
// and DeleteFrom convenience functions, all removed since (commit "remove
// superseded ORM facades") as part of an unrelated, still in-flight API
// cleanup. NewCreatePlan, NewPatchPlan, NewDeletePlan and CreateTable are
// their current replacements and prove the same rejection.
func TestGeneratedViewRejectsEachMutationIndependently(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repository := filepath.Clean(filepath.Join(filepath.Dir(filename), "../.."))
	users := schema.TableDef{
		Name: "users", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	}
	activeUsers := schema.TableDef{
		Name: "active_users", Kind: schema.ObjectView, Operations: schema.OperationRead,
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	}
	for name, proof := range map[string]struct{ view, table string }{
		"insert": {
			view:  `_, _ = rasql.NewCreatePlan(generated.ActiveUsers())`,
			table: `_, _ = rasql.NewCreatePlan(generated.Users())`,
		},
		"update": {
			view:  `_, _ = rasql.NewPatchPlan(generated.ActiveUsers(), rasql.Predicate{})`,
			table: `_, _ = rasql.NewPatchPlan(generated.Users(), rasql.Predicate{})`,
		},
		"delete": {
			view:  `_, _ = rasql.NewDeletePlan(generated.ActiveUsers(), query.Predicate{})`,
			table: `_, _ = rasql.NewDeletePlan(generated.Users(), query.Predicate{})`,
		},
		"ddl": {
			view:  `_ = rasql.CreateTable(ctx, db, generated.ActiveUsers())`,
			table: `_ = rasql.CreateTable(ctx, db, generated.Users())`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			require.NoError(t, compactStore(t, filepath.Join(directory, "generated"), users, activeUsers).Write())
			usage := []byte("package generated_test\n\nimport (\n\t\"context\"\n\t\"github.com/lestrrat-go/rasql\"\n\t\"github.com/lestrrat-go/rasql/query\"\n\t\"example.com/generated/generated\"\n)\n\nvar ctx = context.Background()\nvar db rasql.DB\nfunc proof() {\n" + proof.view + "\n" + proof.table + "\n}\n")
			require.NoError(t, os.WriteFile(filepath.Join(directory, "usage_test.go"), usage, 0o600))
			module := "module example.com/generated\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
			require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte(module), 0o600))
			command := exec.Command("go", "test", "./...")
			command.Dir = directory
			output, err := command.CombinedOutput()
			require.Error(t, err, "mutation unexpectedly compiled:\n%s", output)
		})
	}
}
