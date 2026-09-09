package generate_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lestrrat-go/rasql/internal/scratchmod"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestSchemaDescriptorRoundTripsThroughGeneratedSource proves that the
// schema.TableDef literal the compact emitter writes into a table's own
// generated file is a lossless serialization of the schema.TableDef it came
// from: that a table's descriptor survives being rendered as Go source,
// compiled, read back through the generated package's own public surface,
// and re-rendered into byte-for-byte the same source it first came from.
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
// The first check, that the read-back value is fmt's "%#v" identical to what
// the literal declares, runs inside a small _test.go this test writes
// alongside the generated package, in the same package, so it can compare
// against the unexported literal variable directly. "%#v" rather than
// encoding/json: a JSON struct tag can silently drop a field from the
// comparison -- this test's own history includes exactly that -- while "%#v"
// is reflection over every field of every struct it reaches, exported or
// not.
//
// The second check re-renders both read-back descriptors through
// generate.DescriptorSource -- called from inside that same small program,
// since only it can reach generated.Widgets and generated.Owners -- and
// diffs the result byte-for-byte against the first rendering, which this
// test captures before compact_table_fixture_test.go's helper ever writes
// it to disk. DescriptorSource is the entry point package generate did not
// have until this task added it: the compact rendering pipeline it drives
// lives in internal/compilerir and internal/schemagen, neither importable
// outside this module, so a consumer reaching only generate's exported
// surface could not otherwise reproduce this half of the check.
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
	require.NoError(t, scratchmod.Write(directory, repository, "example.com/roundtrip"))
	packageDir := filepath.Join(directory, "generated")
	store := compactPackageStore(t, packageDir, widgets, owners)
	plan, err := store.Plan()
	require.NoError(t, err)
	var firstRendering []byte
	for _, file := range plan.Files() {
		firstRendering = append(firstRendering, file.Source...)
	}
	require.NoError(t, plan.Commit())

	check := "package generated\n\n" +
		"import (\n\t\"fmt\"\n\t\"testing\"\n\n\t\"github.com/lestrrat-go/rasql/generate\"\n\t\"github.com/lestrrat-go/rasql/schema\"\n)\n\n" +
		"func TestDescriptorRoundTrip(t *testing.T) {\n" +
		"\tgot := Widgets().Ref().Definition()\n" +
		"\tif gotFingerprint := fmt.Sprintf(\"%#v\", got); gotFingerprint != " + strconv.Quote(fingerprint) + " {\n" +
		"\t\tt.Fatalf(\"descriptor read back through Widgets().Ref().Definition() does not match what this package was rendered from:\\nwant %s\\ngot  %s\", " + strconv.Quote(fingerprint) + ", gotFingerprint)\n" +
		"\t}\n" +
		"\tif fmt.Sprintf(\"%#v\", widgetsDefinition) != fmt.Sprintf(\"%#v\", got) {\n" +
		"\t\tt.Fatalf(\"Ref().Definition() is not identical to the literal it clones:\\nliteral %#v\\nclone   %#v\", widgetsDefinition, got)\n" +
		"\t}\n" +
		"\ttables := []schema.TableDef{got, Owners().Ref().Definition()}\n" +
		"\trendered, err := generate.DescriptorSource(\"generated\", tables)\n" +
		"\tif err != nil {\n" +
		"\t\tt.Fatalf(\"re-rendering the read-back descriptors: %s\", err)\n" +
		"\t}\n" +
		"\tif want := " + strconv.Quote(string(firstRendering)) + "; string(rendered) != want {\n" +
		"\t\tt.Fatalf(\"re-rendering the read-back descriptors is not byte-for-byte identical to the first rendering:\\nwant %s\\ngot  %s\", want, rendered)\n" +
		"\t}\n" +
		"}\n"
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "roundtrip_check_test.go"), []byte(check), 0o600))

	command := exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "-run", "TestDescriptorRoundTrip", "./...")
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "descriptor round-trip check output:\n%s", output)
}
