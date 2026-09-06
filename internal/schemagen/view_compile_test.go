package schemagen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestGeneratedViewRejectsEachMutationIndependently(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repository := filepath.Clean(filepath.Join(filepath.Dir(filename), "../.."))
	source, err := schemagen.PackageSource("generated", schema.TableDef{
		Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	}, schema.TableDef{
		Name: "active_users", Kind: schema.ObjectView, Operations: schema.OperationRead,
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	})
	require.NoError(t, err)
	for name, expression := range map[string]string{
		"insert": `_, _ = rasql.Insert(ctx, db, generated.ActiveUsers(), generated.ActiveUsersRow{})`,
		"update": `_, _ = rasql.Update(ctx, db, generated.ActiveUsers(), generated.ActiveUsersRow{})`,
		"delete": `_ = rasql.DeleteFrom(generated.ActiveUsers())`,
		"ddl":    `_ = rasql.CreateTable(ctx, db, generated.ActiveUsers())`,
	} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(directory, "schema.go"), source, 0o600))
			usage := []byte("package generated_test\n\nimport (\n\t\"context\"\n\t\"github.com/lestrrat-go/rasql\"\n\t\"example.com/generated\"\n)\n\nvar ctx = context.Background()\nvar db rasql.DB\nfunc proof() {\n" + expression + "\n_ = generated.Users()\n}\n")
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
