package compilerir_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/schema"
)

func TestPhysicalFromTableDefsClonesNativeFacts(t *testing.T) {
	native := &schema.NativeTypeDef{Dialect: "postgresql", Schema: "app", Name: "user_id", Kind: schema.NativeDomain, Arguments: []string{"uuid"}}
	tables := []schema.TableDef{{Schema: "public", Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.UUIDType{}, NativeType: native}}}}
	catalog, diagnostics := compilerir.PhysicalFromTableDefs(compilerir.EngineIdentity{Dialect: "postgresql"}, tables)
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", diagnostics)
	}
	native.Arguments[0] = "changed"
	tables[0].Columns[0].Name = "changed"
	if got := catalog.Objects[0].Columns[0].Name; got != "id" {
		t.Fatalf("name changed: %q", got)
	}
	if got := catalog.Objects[0].Columns[0].Native.Arguments[0]; got != "uuid" {
		t.Fatalf("native fact changed: %q", got)
	}
}

func TestAssignObjectIDsUsesRenameDestination(t *testing.T) {
	old := compilerir.ObjectID("old-id")
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}, Objects: []compilerir.PhysicalObject{{Kind: "table", Schema: "main", Name: "accounts"}}}
	got, diagnostics := compilerir.AssignObjectIDs(catalog, compilerir.IdentityInput{SourceIdentity: "schema", Prior: []compilerir.PriorObject{{ID: old, Kind: "table", Name: compilerir.QualifiedName{Schema: "main", Name: "users"}}}, Renames: []compilerir.ObjectRename{{ID: old, To: compilerir.QualifiedName{Schema: "main", Name: "accounts"}}}})
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", diagnostics)
	}
	if got.Objects[0].ID != old {
		t.Fatalf("rename did not preserve ID: %q", got.Objects[0].ID)
	}
}

func TestBuildSemanticRejectsUnmappedOpaqueType(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}, Objects: []compilerir.PhysicalObject{{ID: "id", Kind: "table", Name: "events", Columns: []compilerir.PhysicalColumn{{Name: "payload", Ordinal: 0, LogicalKind: "native"}}}}}
	_, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{}, nil)
	if len(diagnostics) == 0 || diagnostics[0].Code != "opaque_type" {
		t.Fatalf("missing opaque diagnostic: %#v", diagnostics)
	}
}
