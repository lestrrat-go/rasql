package generate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestTypedQuerySchemaChangeBreaksStaleCaller(t *testing.T) {
	dir := t.TempDir()
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	module := "module example.com/schema-change\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(root) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0o600))
	caller := filepath.Join(dir, "caller.go")
	writeSchema := func(column schema.ColumnType) {
		source, sourceErr := schemagen.PackageSourceInDir(dir, "generated", schema.TableDef{Name: "users", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: column}}})
		require.NoError(t, sourceErr)
		generated := filepath.Join(dir, "generated")
		require.NoError(t, os.MkdirAll(generated, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(generated, "schema_gen.go"), source, 0o600))
	}
	writeCaller := func(value string) {
		source := "package caller\n\nimport (\n\t\"github.com/lestrrat-go/rasql/query\"\n\t\"example.com/schema-change/generated\"\n)\n\nvar _ = query.EqualValue(generated.Users().ID(), " + value + ")\n"
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
