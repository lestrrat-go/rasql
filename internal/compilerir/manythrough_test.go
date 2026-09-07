package compilerir_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
)

func TestManyThroughMappingBuildsSemanticAndGoModels(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "postgresql"}}
	catalog.Objects = []compilerir.PhysicalObject{
		{ID: "users", Kind: "table", Schema: "public", Name: "users", Columns: []compilerir.PhysicalColumn{{Name: "id", Ordinal: 0, LogicalKind: "integer"}}},
		{ID: "roles", Kind: "table", Schema: "public", Name: "roles", Columns: []compilerir.PhysicalColumn{{Name: "id", Ordinal: 0, LogicalKind: "integer"}}},
	}
	junction := compilerir.PhysicalObject{ID: "user_roles", Kind: "table", Schema: "public", Name: "user_roles"}
	junction.Columns = []compilerir.PhysicalColumn{{Name: "user_id", Ordinal: 0, LogicalKind: "integer"}, {Name: "role_id", Ordinal: 1, LogicalKind: "integer"}}
	junction.Constraints = []compilerir.PhysicalConstraint{
		{Kind: "foreign_key", Name: "user_roles_user", Columns: []string{"user_id"}, Reference: &compilerir.ForeignReference{Schema: "public", Object: "users", Columns: []string{"id"}}},
		{Kind: "foreign_key", Name: "user_roles_role", Columns: []string{"role_id"}, Reference: &compilerir.ForeignReference{Schema: "public", Object: "roles", Columns: []string{"id"}}},
	}
	catalog.Objects = append(catalog.Objects, junction)
	mapping := compilerir.MappingConfig{Relations: []compilerir.RelationMapping{{Name: "roles", Source: "users", From: []string{"id"}, Target: "roles", To: []string{"id"}, Through: compilerir.ThroughMapping{Object: "user_roles", SourceFrom: []string{"user_id"}, SourceTo: []string{"id"}, TargetFrom: []string{"role_id"}, TargetTo: []string{"id"}}}}}
	model, diagnostics := compilerir.BuildSemantic(catalog, mapping, nil)
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", diagnostics)
	}
	relation := model.Objects[0].Relations[0]
	if relation.Kind != "many_through" || relation.Through == nil || relation.Through.Object != "user_roles" {
		t.Fatalf("many-through relation was not retained: %#v", relation)
	}
	goModel, diagnostics := compilerir.BuildGo(model, compilerir.GoConfig{Package: "store", Output: "out"})
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected Go diagnostics: %#v", diagnostics)
	}
	if goModel.Objects[0].Relations[0].Through == nil || goModel.Objects[0].Relations[0].Through.Object != "user_roles" {
		t.Fatalf("Go relation lost through metadata: %#v", goModel.Objects[0].Relations[0])
	}
}

func TestManyThroughMappingRejectsInvalidForeignKeyAndView(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}, Objects: []compilerir.PhysicalObject{{ID: "a", Kind: "view", Name: "a"}, {ID: "b", Kind: "table", Name: "b"}, {ID: "j", Kind: "table", Name: "j"}}}
	mapping := compilerir.MappingConfig{Relations: []compilerir.RelationMapping{{Name: "b", Source: "a", From: []string{"id"}, Target: "b", To: []string{"id"}, Through: compilerir.ThroughMapping{Object: "j", SourceFrom: []string{"id"}, SourceTo: []string{"a_id"}, TargetFrom: []string{"id"}, TargetTo: []string{"b_id"}}}}}
	_, diagnostics := compilerir.BuildSemantic(catalog, mapping, nil)
	if len(diagnostics) == 0 {
		t.Fatal("invalid many-through mapping was accepted")
	}
}
