package schemagen_test

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/scratchmod"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// updateGeneratedShape rewrites the files under testdata/generated_shape from
// the emitter: go test ./internal/schemagen/ -run TestGeneratedShape
// -update-golden. It mirrors go test ./examples/ -update-docs.
var updateGeneratedShape = flag.Bool("update-golden", false, "rewrite testdata/generated_shape from the emitter instead of comparing against it")

// TestGeneratedShape writes out, and checks, the whole file the emitter
// produces for three descriptors, so that reading testdata/generated_shape is
// reading the generator's own output rather than a description of it.
//
// The three are the cases that differ from each other: a base table permitting
// every operation, a view permitting only reads, and a table whose descriptor
// names read and insert and nothing else. The method set follows the
// descriptor's Operations -- a Create method for schema.OperationInsert, a
// Patch method for schema.OperationUpdate, a Delete method for
// schema.OperationDelete -- so the second gets none of the three and the third
// gets only Create.
//
// The wrapper holds its rasql.Table in an unexported field, promoted through a
// package-private alias so As, Column and the CatalogObject methods stay
// reachable without exporting the field itself. Ref, Table, As and InSchema
// forward to that field explicitly; there is no Source method.
func TestGeneratedShape(t *testing.T) {
	for _, testcase := range []struct {
		file  string
		table schema.TableDef
	}{
		{file: "tasks_gen.go", table: shapeTasksTable()},
		{file: "active_users_gen.go", table: shapeActiveUsersView()},
		{file: "audit_log_gen.go", table: shapeAuditLogTable()},
	} {
		t.Run(testcase.file, func(t *testing.T) {
			source := compactObjectFile(t, testcase.table, testcase.file)
			path := filepath.Join("testdata", "generated_shape", testcase.file)
			if *updateGeneratedShape {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
				require.NoError(t, os.WriteFile(path, source, 0o600))
				return
			}
			want, err := os.ReadFile(path)
			require.NoError(t, err, "run go test ./internal/schemagen/ -run TestGeneratedShape -update-golden")
			require.Equal(t, string(want), string(source))
		})
	}
}

// TestGeneratedShapeCompiles builds the same three objects into one scratch
// module and exercises the method set each one got, so the files under
// testdata/generated_shape are known to be code a consumer can call rather
// than only bytes that parse. The append-only table is the case nothing else
// compiles: it has a create builder and no patch builder.
func TestGeneratedShapeCompiles(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repository := filepath.Clean(filepath.Join(filepath.Dir(filename), "../.."))
	directory := t.TempDir()
	require.NoError(t, compactStore(t, filepath.Join(directory, "generated"),
		shapeTasksTable(), shapeActiveUsersView(), shapeAuditLogTable()).Write())

	usage := "package generated_test\n\n" +
		"import (\n\t\"testing\"\n\n\t\"github.com/lestrrat-go/rasql\"\n\t\"example.com/generated/generated\"\n)\n\n" +
		"func TestShape(t *testing.T) {\n" +
		"\tif _, err := generated.Tasks().As(\"t\"); err != nil {\n\t\tt.Fatal(err)\n\t}\n" +
		"\tif _, err := generated.Tasks().Create().ID(1).Title(\"write it down\").Plan(); err != nil {\n\t\tt.Fatal(err)\n\t}\n" +
		"\tif _, err := generated.Tasks().Patch().Title(\"write it down\").Where(rasql.Predicate{}); err == nil {\n\t\tt.Fatal(\"a patch with no predicate was accepted\")\n\t}\n" +
		"\tif _, err := generated.Tasks().Delete(rasql.Predicate{}); err == nil {\n\t\tt.Fatal(\"a delete with no predicate was accepted\")\n\t}\n" +
		"\tmoved, err := generated.Tasks().InSchema(\"tenant\")\n\tif err != nil {\n\t\tt.Fatal(err)\n\t}\n" +
		"\tif moved.Ref().Definition().Name != \"tasks\" {\n\t\tt.Fatal(\"InSchema changed the table name\")\n\t}\n" +
		"\tif _, err := generated.ActiveUsers().As(\"v\"); err != nil {\n\t\tt.Fatal(err)\n\t}\n" +
		"\tif _, err := generated.AuditLog().As(\"a\"); err != nil {\n\t\tt.Fatal(err)\n\t}\n" +
		"\tif _, err := generated.AuditLog().Create().ID(1).Action(\"login\").Plan(); err != nil {\n\t\tt.Fatal(err)\n\t}\n" +
		"}\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, "usage_test.go"), []byte(usage), 0o600))
	require.NoError(t, scratchmod.Write(directory, repository, "example.com/generated"))
	command := exec.Command("go", "test", "./...")
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoError(t, err, "generated shape consumer failed:\n%s", output)
}

// compactObjectFile renders table through the compact emitter and returns the
// one planned file named file, leaving out the runtime helpers every store
// shares.
func compactObjectFile(t *testing.T, table schema.TableDef, file string) []byte {
	t.Helper()

	plan, err := compactStore(t, t.TempDir(), table).Plan()
	require.NoError(t, err)
	for _, planned := range plan.Files() {
		if strings.HasSuffix(filepath.ToSlash(planned.Path), "/"+file) {
			return planned.Source
		}
	}
	t.Fatalf("the plan wrote no %s", file)
	return nil
}

// shapeTasksTable is an ordinary base table. It states no Operations, so
// schema.TableDef.Supports expands the unset field from the object's Kind and
// permits all five.
func shapeTasksTable() schema.TableDef {
	return schema.TableDef{
		Name:       "tasks",
		PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "title", Type: schema.TextType{}},
			{Name: "status", Type: schema.TextType{}, Default: "'open'"},
			{Name: "assignee_id", Type: schema.IntegerType{}, Nullable: true},
		},
	}
}

// shapeActiveUsersView is a view. inspect stamps OperationRead on one it reads
// back from a server, and that bit now survives the compiler IR and reaches
// the emitted descriptor.
func shapeActiveUsersView() schema.TableDef {
	return schema.TableDef{
		Name:       "active_users",
		Kind:       schema.ObjectView,
		Operations: schema.OperationRead,
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
	}
}

// shapeAuditLogTable names read and insert and nothing else: a table rows are
// appended to and never updated or deleted from. requireWritableDefinition
// refused this descriptor outright, so no store could hold one.
func shapeAuditLogTable() schema.TableDef {
	return schema.TableDef{
		Name:       "audit_log",
		Operations: schema.OperationRead | schema.OperationInsert,
		PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "action", Type: schema.TextType{}},
		},
	}
}
