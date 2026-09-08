package generate_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestSchemaDescriptorRoundTripsThroughGeneratedSource proves that the
// schema.TableDef literal the compact emitter writes into a table's own
// generated file is a lossless serialization of the schema.TableDef it came
// from: that a table's descriptor survives being rendered as Go source,
// compiled, and read back through the generated package's own public
// surface.
//
// It cannot reach the descriptor the way the deleted legacy-emitter version
// of this test did, by calling a dedicated per-table "XxxDef() schema.TableDef"
// accessor -- the compact emitter declares no such accessor, only the
// unexported literal variable a table's rasql.Table wrapper is built from.
// generated.Widgets().Ref().Definition() is the compact emitter's equivalent
// path back to a schema.TableDef, so this test uses that instead: it goes
// through more machinery (constructing the wrapper, then unwinding it again
// through query.TableRef), but it is public API a real caller already
// depends on for the same purpose, not something invented for this test.
//
// The check that the read-back value is fmt's "%#v" identical to what the
// literal declares runs inside a small _test.go this test writes alongside
// the generated package, in the same package, so it can compare against the
// unexported literal variable directly. "%#v" rather than encoding/json: a
// JSON struct tag can silently drop a field from the comparison -- this
// test's own history includes exactly that -- while "%#v" is reflection over
// every field of every struct it reaches, exported or not.
//
// What this version does not check, and the deleted version did: it does not
// re-render the read-back descriptor and diff that rendering byte-for-byte
// against the first rendering. That used generate.DescriptorSource, called
// from a small program compiled into its own module that could still reach
// only the exported API of a scratch replace-directive module. Compact
// rendering lives in internal/compilerir and internal/schemagen, neither
// importable outside this module, so reproducing that check would need a new
// exported entry point in package generate that renders a descriptor from a
// plain []schema.TableDef the way generate.DescriptorSource used to. This
// task does not add one; see the task report for what it would need to do.
func TestSchemaDescriptorRoundTripsThroughGeneratedSource(t *testing.T) {
	widgets := schema.TableDef{
		Name:       "widgets",
		Kind:       schema.ObjectTable,
		PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{Unsigned: true}, Identity: schema.IdentityAlways},
			{Name: "name", Type: schema.TextType{Width: schema.NewTextWidth(255)}, Nullable: true, Default: "'unknown'", Collation: "NOCASE"},
			{Name: "amount", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2)}},
			{Name: "owner_id", Type: schema.IntegerType{}},
		},
		UniqueConstraints: []schema.UniqueDef{{Name: "uq_widgets_name", Columns: []string{"name"}}},
		Checks:            []schema.CheckDef{{Name: "chk_widgets_amount", Expression: "amount >= 0"}},
		Indexes:           []schema.IndexDef{{Name: "widgets_owner_idx", Columns: []string{"owner_id"}}},
		ForeignKeys: []schema.ForeignKeyDef{
			{Name: "fk_widgets_owner", Columns: []string{"owner_id"}, ReferencedTable: "owners", ReferencedColumns: []string{"id"}},
		},
	}
	owners := schema.TableDef{Name: "owners", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}}

	require.NoError(t, widgets.Validate(), "fixture must itself be a valid descriptor")
	fingerprint := fmt.Sprintf("%#v", widgets)

	directory := t.TempDir()
	repository, err := filepath.Abs("..")
	require.NoError(t, err)
	module := "module example.com/roundtrip\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte(module), 0o600))
	packageDir := filepath.Join(directory, "generated")
	require.NoError(t, compactPackageStore(t, packageDir, widgets, owners).Write())

	check := "package generated\n\n" +
		"import (\n\t\"fmt\"\n\t\"testing\"\n)\n\n" +
		"func TestDescriptorRoundTrip(t *testing.T) {\n" +
		"\tgot := Widgets().Ref().Definition()\n" +
		"\tif gotFingerprint := fmt.Sprintf(\"%#v\", got); gotFingerprint != " + strconv.Quote(fingerprint) + " {\n" +
		"\t\tt.Fatalf(\"descriptor read back through Widgets().Ref().Definition() does not match what this package was rendered from:\\nwant %s\\ngot  %s\", " + strconv.Quote(fingerprint) + ", gotFingerprint)\n" +
		"\t}\n" +
		"\tif fmt.Sprintf(\"%#v\", widgetsDefinition) != fmt.Sprintf(\"%#v\", got) {\n" +
		"\t\tt.Fatalf(\"Ref().Definition() is not identical to the literal it clones:\\nliteral %#v\\nclone   %#v\", widgetsDefinition, got)\n" +
		"\t}\n" +
		"}\n"
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "roundtrip_check_test.go"), []byte(check), 0o600))

	command := exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "-run", "TestDescriptorRoundTrip", "./...")
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "descriptor round-trip check output:\n%s", output)
}
