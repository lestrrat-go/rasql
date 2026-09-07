package generate

import (
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/schema"
	"testing"
)

func TestCompilerInputFromTableDefsPreservesLegacySidecar(t *testing.T) {
	table := schema.TableDef{Schema: "public", Name: "users", Operations: schema.OperationRead, RowName: "PersonRow", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}, GoBinding: &schema.GoBinding{Type: "int32", Imports: []schema.GoImport{{Path: "example.com/id"}}}}}, Relationships: []schema.RelationshipDef{{Name: "parent", Kind: schema.RelationshipBelongsTo, Columns: []string{"id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}}}}
	input, diagnostics := CompilerInputFromTableDefs(compilerir.EngineIdentity{Dialect: "sqlite"}, []schema.TableDef{table})
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	tables, diagnostics := compilerir.TableDefsFromPhysical(input.Catalog)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	restored, err := restoreLegacy(tables, input)
	if err != nil {
		t.Fatal(err)
	}
	if restored[0].RowName != "PersonRow" {
		t.Fatal("sidecar did not restore row policy")
	}
	if input.Legacy == nil || len(input.Legacy.Objects) != 1 || input.Legacy.Objects[0].RowName != "PersonRow" {
		t.Fatalf("sidecar missing: %#v", input.Legacy)
	}
	if input.Legacy.Objects[0].Columns[0].GoBinding.Type != "int32" {
		t.Fatal("binding missing")
	}
	table.Relationships[0].Columns[0] = "mutated"
	if input.Legacy.Objects[0].Relationships[0].Columns[0] != "id" {
		t.Fatal("sidecar relationship aliases source")
	}
}

func TestStoreRejectsInvalidCompilerInput(t *testing.T) {
	store := Store{Package: "store", Root: t.TempDir(), Dir: "generated", CompilerInput: &CompilerInput{Catalog: compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}, Objects: []compilerir.PhysicalObject{{Kind: "table", Name: "events"}}}}}
	if _, err := store.Plan(); err == nil {
		t.Fatal("invalid compiler input was accepted")
	}
}
