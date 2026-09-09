package generate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/scratchmod"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestTypedQuerySchemaChangeBreaksStaleCaller(t *testing.T) {
	dir := t.TempDir()
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	require.NoError(t, scratchmod.Write(dir, root, "example.com/schema-change"))
	caller := filepath.Join(dir, "caller.go")
	generated := filepath.Join(dir, "generated")
	writeSchema := func(column schema.ColumnType) {
		table := schema.TableDef{Name: "users", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: column}}}
		require.NoError(t, compactPackageStore(t, generated, table).Write())
	}
	writeCaller := func(value string) {
		source := "package caller\n\nimport (\n\t\"github.com/lestrrat-go/rasql\"\n\t\"example.com/schema-change/generated\"\n)\n\nfunc predicate() rasql.Predicate {\n\tsource, err := generated.Users().Source(\"\")\n\tif err != nil {\n\t\tpanic(err)\n\t}\n\tcolumns, err := (generated.UsersColumns{}).Bind(source)\n\tif err != nil {\n\t\tpanic(err)\n\t}\n\treturn rasql.EqualValue(columns.ID.Expr(), " + value + ")\n}\n"
		require.NoError(t, os.WriteFile(caller, []byte(source), 0o600))
	}
	run := func() ([]byte, error) {
		command := exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "-run", "^$", "./...")
		command.Dir = dir
		output, runErr := command.CombinedOutput()
		return output, runErr
	}
	writeSchema(schema.IntegerType{})
	writeCaller("int64(7)")
	output, err := run()
	require.NoErrorf(t, err, "integer caller output:\n%s", output)
	writeSchema(schema.TextType{})
	output, err = run()
	require.Error(t, err)
	require.Contains(t, string(output), "does not match inferred type string")
	writeCaller(`"7"`)
	output, err = run()
	require.NoErrorf(t, err, "text caller output:\n%s", output)
}
