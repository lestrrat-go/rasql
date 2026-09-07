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

func TestManyThroughValidationKeepsSourceAndTargetWidthsIndependent(t *testing.T) {
	column := func(name string) compilerir.SemanticColumn {
		return compilerir.SemanticColumn{Name: name, Scalar: "integer", Readable: true, InsertState: "required", PatchState: "settable", Certainty: compilerir.CertaintyKnown}
	}
	model := compilerir.SemanticModel{Objects: []compilerir.SemanticObject{
		{ID: "users", Kind: "table", PhysicalName: compilerir.QualifiedName{Name: "users"}, Columns: []compilerir.SemanticColumn{column("id")}, Relations: []compilerir.SemanticRelation{{Name: "Roles", Kind: "many_through", Target: "roles", From: []string{"id"}, To: []string{"tenant_id", "id"}, Through: &compilerir.SemanticThrough{Object: "links", SourceFrom: []string{"user_id"}, SourceTo: []string{"id"}, TargetFrom: []string{"role_tenant", "role_id"}, TargetTo: []string{"tenant_id", "id"}}}}},
		{ID: "roles", Kind: "table", PhysicalName: compilerir.QualifiedName{Name: "roles"}, Columns: []compilerir.SemanticColumn{column("tenant_id"), column("id")}},
		{ID: "links", Kind: "table", PhysicalName: compilerir.QualifiedName{Name: "links"}, Columns: []compilerir.SemanticColumn{column("user_id"), column("role_tenant"), column("role_id")}},
	}}
	if err := compilerir.ValidateSemantic(model); err != nil {
		t.Fatalf("independent relation widths were rejected: %v", err)
	}
	goModel, diagnostics := compilerir.BuildGo(model, compilerir.GoConfig{Package: "store"})
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected Go diagnostics: %#v", diagnostics)
	}
	if err := compilerir.ValidateGo(goModel); err != nil {
		t.Fatalf("Go relation paths were rejected: %v", err)
	}
}

func TestManyThroughValidationRejectsDuplicateAndUnknownMetadata(t *testing.T) {
	column := func(name string) compilerir.SemanticColumn {
		return compilerir.SemanticColumn{Name: name, Scalar: "integer", Readable: true, InsertState: "required", PatchState: "settable", Certainty: compilerir.CertaintyKnown}
	}
	model := compilerir.SemanticModel{Objects: []compilerir.SemanticObject{
		{ID: "users", Kind: "table", PhysicalName: compilerir.QualifiedName{Name: "users"}, Columns: []compilerir.SemanticColumn{column("id")}, Relations: []compilerir.SemanticRelation{
			{Name: "Roles", Kind: "many_through", Target: "roles", From: []string{"id"}, To: []string{"id"}, Through: &compilerir.SemanticThrough{Object: "links", SourceFrom: []string{"user_id"}, SourceTo: []string{"id"}, TargetFrom: []string{"role_id"}, TargetTo: []string{"id"}}},
			{Name: "Roles", Kind: "many_through", Target: "roles", From: []string{"id"}, To: []string{"id"}, Through: &compilerir.SemanticThrough{Object: "links", SourceFrom: []string{"user_id"}, SourceTo: []string{"id"}, TargetFrom: []string{"role_id"}, TargetTo: []string{"id"}}},
		}},
		{ID: "roles", Kind: "table", PhysicalName: compilerir.QualifiedName{Name: "roles"}, Columns: []compilerir.SemanticColumn{column("id")}},
		{ID: "links", Kind: "table", PhysicalName: compilerir.QualifiedName{Name: "links"}, Columns: []compilerir.SemanticColumn{column("user_id"), column("role_id")}},
	}}
	if err := compilerir.ValidateSemantic(model); err == nil {
		t.Fatal("duplicate relation names were accepted")
	}
	model.Objects[0].Relations = model.Objects[0].Relations[:1]
	model.Objects[0].Relations[0].Through.Object = "missing"
	if err := compilerir.ValidateSemantic(model); err == nil {
		t.Fatal("unknown through object was accepted")
	}
}

func TestManyThroughMappingRejectsDerivedRelationNameCollision(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}, Objects: []compilerir.PhysicalObject{
		{ID: "users", Kind: "table", Name: "users", Columns: []compilerir.PhysicalColumn{{Name: "id", Ordinal: 0, LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{{Kind: "foreign_key", Name: "Roles", Columns: []string{"id"}, Reference: &compilerir.ForeignReference{Object: "roles", Columns: []string{"id"}}}}},
		{ID: "roles", Kind: "table", Name: "roles", Columns: []compilerir.PhysicalColumn{{Name: "id", Ordinal: 0, LogicalKind: "integer"}}},
		{ID: "links", Kind: "table", Name: "links", Columns: []compilerir.PhysicalColumn{{Name: "user_id", Ordinal: 0, LogicalKind: "integer"}, {Name: "role_id", Ordinal: 1, LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{
			{Kind: "foreign_key", Name: "links_user", Columns: []string{"user_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}},
			{Kind: "foreign_key", Name: "links_role", Columns: []string{"role_id"}, Reference: &compilerir.ForeignReference{Object: "roles", Columns: []string{"id"}}},
		}},
	}}
	mapping := compilerir.MappingConfig{Relations: []compilerir.RelationMapping{{Name: "Roles", Source: "users", From: []string{"id"}, Target: "roles", To: []string{"id"}, Through: compilerir.ThroughMapping{Object: "links", SourceFrom: []string{"user_id"}, SourceTo: []string{"id"}, TargetFrom: []string{"role_id"}, TargetTo: []string{"id"}}}}}
	_, diagnostics := compilerir.BuildSemantic(catalog, mapping, nil)
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "relation_name_collision" && diagnostic.Path == "mappings.relations[0].name" {
			return
		}
	}
	t.Fatalf("derived relation collision was not diagnosed: %#v", diagnostics)
}

func TestManyThroughMappingSupportsSelfTableWithTwoJunctionRoles(t *testing.T) {
	users := compilerir.PhysicalObject{ID: "users", Kind: "table", Name: "users", Columns: []compilerir.PhysicalColumn{{Name: "id", Ordinal: 0, LogicalKind: "integer"}}}
	links := compilerir.PhysicalObject{ID: "links", Kind: "table", Name: "links", Columns: []compilerir.PhysicalColumn{{Name: "source_id", Ordinal: 0, LogicalKind: "integer"}, {Name: "target_id", Ordinal: 1, LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{
		{Kind: "foreign_key", Name: "links_source", Columns: []string{"source_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}},
		{Kind: "foreign_key", Name: "links_target", Columns: []string{"target_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}},
	}}
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}, Objects: []compilerir.PhysicalObject{users, links}}
	mapping := compilerir.MappingConfig{Relations: []compilerir.RelationMapping{{Name: "Friends", Source: "users", From: []string{"id"}, Target: "users", To: []string{"id"}, Through: compilerir.ThroughMapping{Object: "links", SourceFrom: []string{"source_id"}, SourceTo: []string{"id"}, TargetFrom: []string{"target_id"}, TargetTo: []string{"id"}}}}}
	model, diagnostics := compilerir.BuildSemantic(catalog, mapping, nil)
	if len(diagnostics) != 0 {
		t.Fatalf("self-table mapping was rejected: %#v", diagnostics)
	}
	if len(model.Objects[0].Relations) != 1 || model.Objects[0].Relations[0].Through == nil {
		t.Fatalf("self-table relation was not retained: %#v", model.Objects[0].Relations)
	}
}

func TestManyThroughMappingRejectsWrongJunctionPathsAndTypes(t *testing.T) {
	base := manyThroughCatalog()
	mapping := compilerir.RelationMapping{Name: "Roles", Source: "users", From: []string{"id"}, Target: "roles", To: []string{"id"}, Through: compilerir.ThroughMapping{Object: "user_roles", SourceFrom: []string{"user_id"}, SourceTo: []string{"id"}, TargetFrom: []string{"role_id"}, TargetTo: []string{"id"}}}
	cases := map[string]func(*compilerir.PhysicalCatalog, *compilerir.RelationMapping){
		"reversed source foreign key": func(_ *compilerir.PhysicalCatalog, value *compilerir.RelationMapping) {
			value.Through.SourceFrom[0] = "role_id"
		},
		"reversed target foreign key": func(_ *compilerir.PhysicalCatalog, value *compilerir.RelationMapping) {
			value.Through.TargetFrom[0] = "user_id"
		},
		"incompatible target type": func(catalog *compilerir.PhysicalCatalog, _ *compilerir.RelationMapping) {
			catalog.Objects[1].Columns[0].LogicalKind = "text"
		},
		"missing junction": func(_ *compilerir.PhysicalCatalog, value *compilerir.RelationMapping) {
			value.Through.Object = "missing"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			catalog := base.Clone()
			candidate := mapping
			candidate.From = append([]string(nil), mapping.From...)
			candidate.To = append([]string(nil), mapping.To...)
			candidate.Through.SourceFrom = append([]string(nil), mapping.Through.SourceFrom...)
			candidate.Through.SourceTo = append([]string(nil), mapping.Through.SourceTo...)
			candidate.Through.TargetFrom = append([]string(nil), mapping.Through.TargetFrom...)
			candidate.Through.TargetTo = append([]string(nil), mapping.Through.TargetTo...)
			mutate(&catalog, &candidate)
			_, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{Relations: []compilerir.RelationMapping{candidate}}, nil)
			if len(diagnostics) == 0 {
				t.Fatal("invalid junction metadata was accepted")
			}
		})
	}
}

func manyThroughCatalog() compilerir.PhysicalCatalog {
	return compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "postgresql"}, Objects: []compilerir.PhysicalObject{
		{ID: "users", Kind: "table", Name: "users", Columns: []compilerir.PhysicalColumn{{Name: "id", Ordinal: 0, LogicalKind: "integer"}}},
		{ID: "roles", Kind: "table", Name: "roles", Columns: []compilerir.PhysicalColumn{{Name: "id", Ordinal: 0, LogicalKind: "integer"}}},
		{ID: "user_roles", Kind: "table", Name: "user_roles", Columns: []compilerir.PhysicalColumn{{Name: "user_id", Ordinal: 0, LogicalKind: "integer"}, {Name: "role_id", Ordinal: 1, LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{
			{Kind: "foreign_key", Name: "user_roles_user", Columns: []string{"user_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}},
			{Kind: "foreign_key", Name: "user_roles_role", Columns: []string{"role_id"}, Reference: &compilerir.ForeignReference{Object: "roles", Columns: []string{"id"}}},
		}},
	}}
}
