package compilerir_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
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

func TestCatalogFixturesLoadPhysicalFacts(t *testing.T) {
	for _, dialect := range []string{"postgresql", "mysql", "sqlite"} {
		dialect := dialect
		t.Run(dialect, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", dialect, "catalog.json"))
			if err != nil {
				t.Fatal(err)
			}
			var fixture struct {
				Engine  compilerir.EngineIdentity   `json:"engine"`
				Objects []compilerir.PhysicalObject `json:"objects"`
			}
			if err := json.Unmarshal(data, &fixture); err != nil {
				t.Fatal(err)
			}
			if fixture.Engine.Dialect != dialect || len(fixture.Objects) == 0 {
				t.Fatalf("fixture lacks engine or objects: %#v", fixture)
			}
			catalog := compilerir.PhysicalCatalog{Engine: fixture.Engine, Objects: fixture.Objects}
			if err := catalog.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPhysicalRoundTripPreservesCompositeConstraintsAndDefaults(t *testing.T) {
	table := schema.TableDef{Schema: "public", Name: "orders", Strict: true, WithoutRowID: true, Columns: []schema.ColumnDef{{Name: "tenant", Type: schema.IntegerType{}, Nullable: false, Default: "1"}, {Name: "id", Type: schema.IntegerType{}, Nullable: false}, {Name: "note", Type: schema.TextType{Width: schema.NewTextWidth(0)}, Nullable: true}}, PrimaryKey: []string{"tenant", "id"}, UniqueConstraints: []schema.UniqueDef{{Name: "orders_note_key", Columns: []string{"note"}}}, Checks: []schema.CheckDef{{Name: "note_check", Expression: "note IS NULL OR note <> ''"}}, ForeignKeys: []schema.ForeignKeyDef{{Name: "orders_parent", Columns: []string{"tenant", "id"}, ReferencedSchema: "public", ReferencedTable: "orders", ReferencedColumns: []string{"tenant", "id"}}}, Indexes: []schema.IndexDef{{Name: "orders_note_idx", Expressions: []sqltext.Text{"lower(note)"}}}, ExclusionConstraints: []schema.ExclusionDef{{Name: "orders_excl", Method: "gist", Elements: []schema.ExclusionElementDef{{Expression: "id", Operator: "="}}}}}
	first, diagnostics := compilerir.PhysicalFromTableDefs(compilerir.EngineIdentity{Dialect: "postgresql"}, []schema.TableDef{table})
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	secondTables, diagnostics := compilerir.TableDefsFromPhysical(first)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	second, diagnostics := compilerir.PhysicalFromTableDefs(compilerir.EngineIdentity{Dialect: "postgresql"}, secondTables)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if len(second.Objects) != 1 || len(second.Objects[0].Constraints) != 4 || len(second.Objects[0].Constraints[0].Columns) != 2 {
		t.Fatalf("composite facts lost: %#v", second.Objects)
	}
	if second.Objects[0].Columns[0].DefaultSQL != "1" || len(second.Objects[0].ExclusionConstraints) != 1 {
		t.Fatalf("roundtrip facts lost: %#v", second.Objects[0])
	}
}

func TestPhysicalCloneDeepCopiesFactPointersAndExclusions(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}, Objects: []compilerir.PhysicalObject{{ID: "id", Kind: "table", Name: "t", VirtualTableModuleArguments: []string{"x"}, Columns: []compilerir.PhysicalColumn{{Name: "n", Ordinal: 0, LogicalKind: "integer", Integer: &compilerir.IntegerTypeFacts{DisplayWidth: compilerir.OptionalInt{Value: 0, Set: true}}, Native: &compilerir.NativeType{Arguments: []string{}}}}, ExclusionConstraints: []compilerir.PhysicalExclusionConstraint{{Elements: []compilerir.ExclusionElement{{ExpressionSQL: "x"}}}}}}}
	clone := catalog.Clone()
	clone.Objects[0].Columns[0].Integer.DisplayWidth.Value = 9
	clone.Objects[0].Columns[0].Native.Arguments = append(clone.Objects[0].Columns[0].Native.Arguments, "x")
	clone.Objects[0].ExclusionConstraints[0].Elements[0].ExpressionSQL = "y"
	clone.Objects[0].VirtualTableModuleArguments[0] = "y"
	if catalog.Objects[0].Columns[0].Integer.DisplayWidth.Value != 0 || len(catalog.Objects[0].Columns[0].Native.Arguments) != 0 || catalog.Objects[0].ExclusionConstraints[0].Elements[0].ExpressionSQL != "x" || catalog.Objects[0].VirtualTableModuleArguments[0] != "x" {
		t.Fatal("physical clone aliases nested facts")
	}
}

func TestAssignObjectIDsRejectsDuplicateRenameIDs(t *testing.T) {
	_, diagnostics := compilerir.AssignObjectIDs(compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}}, compilerir.IdentityInput{SourceIdentity: "s", Prior: []compilerir.PriorObject{{ID: "id", Kind: "table", Name: compilerir.QualifiedName{Name: "a"}}}, Renames: []compilerir.ObjectRename{{ID: "id", To: compilerir.QualifiedName{Name: "b"}}, {ID: "id", To: compilerir.QualifiedName{Name: "c"}}}})
	found := false
	for _, d := range diagnostics {
		if d.Code == "rename_id_duplicate" {
			found = true
		}
	}
	if !found {
		t.Fatal("duplicate rename ID was accepted")
	}
}

func TestGoClonesCopyImportsAndScalarMappings(t *testing.T) {
	model := compilerir.GoModel{Imports: []compilerir.GoImport{{Path: "x"}}}
	clone := model.Clone()
	clone.Imports[0].Path = "y"
	if model.Imports[0].Path != "x" {
		t.Fatal("GoModel clone aliases imports")
	}
	config := compilerir.GoConfig{Scalars: []compilerir.ScalarMapping{{Name: "x", Imports: []compilerir.GoImport{{Path: "a"}}}}}
	copy := config.Clone()
	copy.Scalars[0].Imports[0].Path = "b"
	if config.Scalars[0].Imports[0].Path != "a" {
		t.Fatal("GoConfig clone aliases mappings")
	}
}

func TestIndexKeyFormRoundTripPreservesKeys(t *testing.T) {
	table := schema.TableDef{Schema: "public", Name: "events", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}, Indexes: []schema.IndexDef{{Name: "events_id_idx", Keys: []schema.IndexKeyDef{{Expression: "id", Descending: true, Collation: "C", OperatorClass: "int4_ops", PrefixLength: 2}}}}}
	catalog, diagnostics := compilerir.PhysicalFromTableDefs(compilerir.EngineIdentity{Dialect: "postgresql"}, []schema.TableDef{table})
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if catalog.Objects[0].Indexes[0].KeyForm != "keys" {
		t.Fatalf("missing key form: %#v", catalog.Objects[0].Indexes[0])
	}
	back, diagnostics := compilerir.TableDefsFromPhysical(catalog)
	if len(diagnostics) != 0 || len(back[0].Indexes) != 1 || len(back[0].Indexes[0].Keys) != 1 {
		t.Fatalf("keys were not restored: %#v %#v", back, diagnostics)
	}
	key := back[0].Indexes[0].Keys[0]
	if !key.Descending || key.Collation != "C" || key.OperatorClass != "int4_ops" || key.PrefixLength != 2 {
		t.Fatalf("key modifiers were lost: %#v", key)
	}
}

func TestBuildGoPreservesQueryShapeAndRejectsUnknownScalarType(t *testing.T) {
	model, diagnostics := compilerir.BuildGo(compilerir.SemanticModel{Queries: []compilerir.SemanticQuery{{ID: "q", Name: "events", Parameters: []compilerir.SemanticValue{{Name: "at", Scalar: "time", TypeCertainty: compilerir.CertaintyKnown, NullabilityCertainty: compilerir.CertaintyKnown}}, Results: []compilerir.SemanticValue{{Name: "id", Scalar: "custom", TypeCertainty: compilerir.CertaintyKnown, NullabilityCertainty: compilerir.CertaintyKnown}}}}}, compilerir.GoConfig{Queries: []compilerir.QueryGoName{{ID: "q", Function: "FindEvents", Result: "EventResult", Decoder: "DecodeEvent", File: "events.go"}}})
	if len(diagnostics) == 0 || model.Queries[0].Name != "FindEvents" || model.Queries[0].Result == nil || model.Queries[0].Result.Name != "EventResult" || model.Queries[0].Result.DecoderName != "DecodeEvent" {
		t.Fatalf("query generation policy was lost: %#v %#v", model, diagnostics)
	}
	if model.Queries[0].Result.Fields[0].Type != "" {
		t.Fatalf("unknown scalar became a Go type: %#v", model.Queries[0].Result.Fields[0])
	}
	if len(model.Files) != 1 || model.Files[0].Path != "events.go" {
		t.Fatalf("query file policy was lost: %#v", model.Files)
	}
}
