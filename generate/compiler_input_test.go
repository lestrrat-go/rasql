package generate_test

import (
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/schema"
)

func TestCompilerInputFromTableDefsPreservesLegacySidecar(t *testing.T) {
	relation := schema.RelationshipDef{Name: "parent", Kind: schema.RelationshipManyToMany, Columns: []string{"id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}, Through: &schema.RelationshipThrough{Table: schema.ObjectName{Name: "links"}, SourceColumns: []string{"id"}, TargetColumns: []string{"id"}}}
	table := schema.TableDef{Schema: "public", Name: "users", Operations: schema.OperationRead, RowName: "PersonRow", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}, GoBinding: &schema.GoBinding{Type: "int32", Imports: []schema.GoImport{{Path: "example.com/id"}}}}}, Relationships: []schema.RelationshipDef{relation}}
	input, diagnostics := generate.CompilerInputFromTableDefs(compilerir.EngineIdentity{Dialect: "sqlite"}, []schema.TableDef{table})
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if input.Legacy == nil || len(input.Legacy.Objects) != 1 || input.Legacy.Objects[0].RowName != "PersonRow" {
		t.Fatalf("sidecar missing: %#v", input.Legacy)
	}
	input.Legacy.Objects[0].Relationships[0].Through.SourceColumns[0] = "changed"
	if relation.Through.SourceColumns[0] != "id" {
		t.Fatal("sidecar relationship aliases source")
	}
	input.Legacy.Objects[0].Columns[0].GoBinding.Imports[0].Path = "changed"
	if table.Columns[0].GoBinding.Imports[0].Path != "example.com/id" {
		t.Fatal("sidecar binding aliases source")
	}
}

func TestStorePlanRejectsInvalidCompilerInput(t *testing.T) {
	input := &generate.CompilerInput{Catalog: compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}, Objects: []compilerir.PhysicalObject{{Kind: "table", Name: "events"}}}}
	store := generate.Store{Package: "store", Root: ".", Dir: "generated", CompilerInput: input}
	if _, err := store.Plan(); err == nil || !strings.Contains(err.Error(), "compiler input") {
		t.Fatalf("unexpected compiler input result: %v", err)
	}
}
