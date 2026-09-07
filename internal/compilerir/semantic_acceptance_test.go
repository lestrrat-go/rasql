package compilerir_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
)

func TestBuildSemanticResolvesLaterAndUnnamedForeignKeys(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}, Objects: []compilerir.PhysicalObject{
		{ID: "orders", Kind: "table", Schema: "main", Name: "orders", Columns: []compilerir.PhysicalColumn{{Name: "user_id", Ordinal: 0, LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{{Kind: "foreign_key", Columns: []string{"user_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}}}},
		{ID: "users", Kind: "table", Schema: "main", Name: "users", Columns: []compilerir.PhysicalColumn{{Name: "id", Ordinal: 0, LogicalKind: "integer"}}},
	}}
	model, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{}, nil)
	if len(diagnostics) != 0 || len(model.Objects[0].Relations) != 1 || model.Objects[0].Relations[0].Target != "users" || model.Objects[0].Relations[0].Name == "" {
		t.Fatalf("unexpected semantic result: model=%#v diagnostics=%#v", model, diagnostics)
	}
}

func TestBuildSemanticReportsAmbiguousScalarMapping(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}, Objects: []compilerir.PhysicalObject{{ID: "t", Kind: "table", Name: "t", Columns: []compilerir.PhysicalColumn{{Name: "id", Ordinal: 0, LogicalKind: "integer"}}}}}
	_, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{{Name: "one", Match: compilerir.NativeMatch{LogicalKind: "integer"}}, {Name: "two", Match: compilerir.NativeMatch{LogicalKind: "integer"}}}}, nil)
	if len(diagnostics) == 0 || diagnostics[0].Code != "ambiguous_scalar" {
		t.Fatalf("expected ambiguous scalar diagnostic, got %#v", diagnostics)
	}
}

func TestBuildSemanticUsesSQLiteSourceNamespaceAndKeepsSQLConstraintNames(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}, Objects: []compilerir.PhysicalObject{
		{ID: "archive-orders", Kind: "table", Schema: "archive", Name: "orders", Columns: []compilerir.PhysicalColumn{{Name: "user_id", Ordinal: 0, LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{{Name: "fk-users", Kind: "foreign_key", Columns: []string{"user_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}}}},
		{ID: "archive-users", Kind: "table", Schema: "archive", Name: "users", Columns: []compilerir.PhysicalColumn{{Name: "id", Ordinal: 0, LogicalKind: "integer"}}},
		{ID: "main-users", Kind: "table", Schema: "main", Name: "users", Columns: []compilerir.PhysicalColumn{{Name: "id", Ordinal: 0, LogicalKind: "integer"}}},
	}}
	model, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{}, nil)
	if len(diagnostics) != 0 || model.Objects[0].Relations[0].Target != "archive-users" || model.Objects[0].Relations[0].Name != "fk-users" {
		t.Fatalf("unexpected result: %#v %#v", model, diagnostics)
	}
}

func TestBuildGoUsesDefaultRasqlImportAlias(t *testing.T) {
	model := compilerir.SemanticModel{Queries: []compilerir.SemanticQuery{{ID: "q", Name: "q", Cardinality: "one", Results: []compilerir.SemanticValue{{Name: "at", Scalar: "time", Nullable: true, TypeCertainty: compilerir.CertaintyKnown, NullabilityCertainty: compilerir.CertaintyKnown}}}}}
	got, diagnostics := compilerir.BuildGo(model, compilerir.GoConfig{Package: "store"})
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	for _, importSpec := range got.Imports {
		if importSpec.Path == "github.com/lestrrat-go/rasql" && importSpec.Alias != "" {
			t.Fatalf("default rasql import unexpectedly has alias %q", importSpec.Alias)
		}
	}
}
