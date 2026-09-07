package compilerir_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
)

func TestBuildSemanticDerivesDirectAndInverseRelations(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "postgresql"}, Objects: []compilerir.PhysicalObject{
		{ID: "tasks", Kind: "table", Name: "tasks", Columns: []compilerir.PhysicalColumn{
			{Name: "id", Ordinal: 0, LogicalKind: "integer"},
			{Name: "assignee_id", Ordinal: 1, LogicalKind: "integer", Nullable: true},
			{Name: "project_id", Ordinal: 2, LogicalKind: "integer"},
		}, Constraints: []compilerir.PhysicalConstraint{
			{Kind: "primary_key", Columns: []string{"id"}},
			{Kind: "foreign_key", Name: "tasks_assignee_fk", Columns: []string{"assignee_id"}, Reference: &compilerir.ForeignReference{Object: "members", Columns: []string{"id"}}},
			{Kind: "foreign_key", Name: "tasks_project_fk", Columns: []string{"project_id"}, Reference: &compilerir.ForeignReference{Object: "projects", Columns: []string{"id"}}},
		}},
		{ID: "members", Kind: "table", Name: "members", Columns: []compilerir.PhysicalColumn{{Name: "id", Ordinal: 0, LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}}},
		{ID: "projects", Kind: "table", Name: "projects", Columns: []compilerir.PhysicalColumn{{Name: "id", Ordinal: 0, LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}}},
	}}
	model, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{}, nil)
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", diagnostics)
	}
	tasks := relationObject(model, "tasks")
	assignee := relation(tasks, "Assignee")
	if assignee.Kind != "belongs_to" || assignee.Target != "members" || !assignee.Nullable || !equalStrings(assignee.From, []string{"assignee_id"}) || !equalStrings(assignee.To, []string{"id"}) {
		t.Fatalf("unexpected assignee relation: %#v", assignee)
	}
	project := relation(tasks, "Project")
	if project.Kind != "belongs_to" || project.Target != "projects" || project.Nullable || !equalStrings(project.From, []string{"project_id"}) || !equalStrings(project.To, []string{"id"}) {
		t.Fatalf("unexpected project relation: %#v", project)
	}
	for _, objectID := range []compilerir.ObjectID{"members", "projects"} {
		inverse := relation(relationObject(model, objectID), "Tasks")
		if inverse.Kind != "has_many" || inverse.Target != "tasks" || !equalStrings(inverse.From, []string{"id"}) || !equalStrings(inverse.To, []string{objectIDName(objectID) + "_id"}) {
			t.Fatalf("unexpected %s inverse relation: %#v", objectID, inverse)
		}
	}
	goModel, diagnostics := compilerir.BuildGo(model, compilerir.GoConfig{Package: "store", Output: "generated"})
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected Go diagnostics: %#v", diagnostics)
	}
	if err := compilerir.ValidateGo(goModel); err != nil {
		t.Fatalf("derived Go relations failed validation: %v", err)
	}
}

func TestBuildSemanticDerivesUniqueAndRoleQualifiedInverseRelations(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "postgresql"}, Objects: []compilerir.PhysicalObject{
		{ID: "tasks", Kind: "table", Name: "tasks", Columns: []compilerir.PhysicalColumn{
			{Name: "id", Ordinal: 0, LogicalKind: "integer"}, {Name: "billing_user_id", Ordinal: 1, LogicalKind: "integer"}, {Name: "shipping_user_id", Ordinal: 2, LogicalKind: "integer"},
		}, Constraints: []compilerir.PhysicalConstraint{
			{Kind: "primary_key", Columns: []string{"id"}},
			{Kind: "foreign_key", Columns: []string{"billing_user_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}},
			{Kind: "foreign_key", Columns: []string{"shipping_user_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}},
		}},
		{ID: "users", Kind: "table", Name: "users", Columns: []compilerir.PhysicalColumn{{Name: "id", Ordinal: 0, LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}}},
	}}
	model, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{}, nil)
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", diagnostics)
	}
	users := relationObject(model, "users")
	if relation(users, "BillingUserTasks").Kind != "has_many" || relation(users, "ShippingUserTasks").Kind != "has_many" {
		t.Fatalf("role-qualified inverse relations were not derived: %#v", users.Relations)
	}

	unique := catalog
	unique.Objects = []compilerir.PhysicalObject{
		{ID: "profiles", Kind: "table", Name: "profiles", Columns: []compilerir.PhysicalColumn{{Name: "id", Ordinal: 0, LogicalKind: "integer"}, {Name: "user_id", Ordinal: 1, LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}, {Kind: "unique", Columns: []string{"user_id"}}, {Kind: "foreign_key", Columns: []string{"user_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}}}},
		catalog.Objects[1],
	}
	model, diagnostics = compilerir.BuildSemantic(unique, compilerir.MappingConfig{}, nil)
	if len(diagnostics) != 0 || relation(relationObject(model, "users"), "Profiles").Kind != "has_one" {
		t.Fatalf("unique child foreign key did not derive has_one: model=%#v diagnostics=%#v", model, diagnostics)
	}
}

func TestBuildSemanticReportsDerivedRelationNameCollision(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}, Objects: []compilerir.PhysicalObject{
		{ID: "tasks", Kind: "table", Name: "tasks", Columns: []compilerir.PhysicalColumn{{Name: "user_id", Ordinal: 0, LogicalKind: "integer"}, {Name: "user", Ordinal: 1, LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{
			{Kind: "foreign_key", Columns: []string{"user_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}},
			{Kind: "foreign_key", Columns: []string{"user"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}},
		}},
		{ID: "users", Kind: "table", Name: "users", Columns: []compilerir.PhysicalColumn{{Name: "id", Ordinal: 0, LogicalKind: "integer"}}},
	}}
	_, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{}, nil)
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "relation_name_collision" {
			return
		}
	}
	t.Fatalf("derived relation collision was not diagnosed: %#v", diagnostics)
}

func TestBuildSemanticReportsCanonicalDirectAndManyThroughNameCollision(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}, Objects: []compilerir.PhysicalObject{
		{ID: "users", Kind: "table", Name: "users", Columns: []compilerir.PhysicalColumn{{Name: "id", LogicalKind: "integer"}, {Name: "role_id", Ordinal: 1, LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}, {Kind: "foreign_key", Columns: []string{"role_id"}, Reference: &compilerir.ForeignReference{Object: "roles", Columns: []string{"id"}}}}},
		{ID: "roles", Kind: "table", Name: "roles", Columns: []compilerir.PhysicalColumn{{Name: "id", LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}}},
		{ID: "user_roles", Kind: "table", Name: "user_roles", Columns: []compilerir.PhysicalColumn{{Name: "user_id", LogicalKind: "integer"}, {Name: "role_id", Ordinal: 1, LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{{Kind: "foreign_key", Columns: []string{"user_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}}, {Kind: "foreign_key", Columns: []string{"role_id"}, Reference: &compilerir.ForeignReference{Object: "roles", Columns: []string{"id"}}}}},
	}}
	mappings := compilerir.MappingConfig{Relations: []compilerir.RelationMapping{{Name: "role", Source: "users", From: []string{"id"}, Target: "roles", To: []string{"id"}, Through: compilerir.ThroughMapping{Object: "user_roles", SourceFrom: []string{"user_id"}, SourceTo: []string{"id"}, TargetFrom: []string{"role_id"}, TargetTo: []string{"id"}}}}}
	model, diagnostics := compilerir.BuildSemantic(catalog, mappings, nil)
	if compilerir.RelationGoName("role") != "Role" || relation(relationObject(model, "users"), "role").Name != "role" {
		t.Fatalf("configured relation label was not preserved: %#v", model)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "relation_name_collision" && diagnostic.Path == "mappings.relations[0].name" {
			return
		}
	}
	t.Fatalf("canonical direct/many-through collision was not diagnosed: %#v", diagnostics)
}

func TestBuildSemanticReportsCanonicalInverseAndManyThroughNameCollision(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}, Objects: []compilerir.PhysicalObject{
		{ID: "users", Kind: "table", Name: "users", Columns: []compilerir.PhysicalColumn{{Name: "id", LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}}},
		{ID: "tasks", Kind: "table", Name: "tasks", Columns: []compilerir.PhysicalColumn{{Name: "id", LogicalKind: "integer"}, {Name: "user_id", Ordinal: 1, LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}, {Kind: "foreign_key", Columns: []string{"user_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}}}},
		{ID: "user_tasks", Kind: "table", Name: "user_tasks", Columns: []compilerir.PhysicalColumn{{Name: "user_id", LogicalKind: "integer"}, {Name: "task_id", Ordinal: 1, LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{{Kind: "foreign_key", Columns: []string{"user_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}}, {Kind: "foreign_key", Columns: []string{"task_id"}, Reference: &compilerir.ForeignReference{Object: "tasks", Columns: []string{"id"}}}}},
	}}
	mappings := compilerir.MappingConfig{Relations: []compilerir.RelationMapping{{Name: "tasks", Source: "users", From: []string{"id"}, Target: "tasks", To: []string{"id"}, Through: compilerir.ThroughMapping{Object: "user_tasks", SourceFrom: []string{"user_id"}, SourceTo: []string{"id"}, TargetFrom: []string{"task_id"}, TargetTo: []string{"id"}}}}}
	_, diagnostics := compilerir.BuildSemantic(catalog, mappings, nil)
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "relation_name_collision" && diagnostic.Path == "mappings.relations[0].name" {
			return
		}
	}
	t.Fatalf("canonical inverse/many-through collision was not diagnosed: %#v", diagnostics)
}

func TestBuildSemanticRelationOrderIgnoresForeignKeyConstraintOrder(t *testing.T) {
	makeCatalog := func(reverse bool) compilerir.PhysicalCatalog {
		constraints := []compilerir.PhysicalConstraint{
			{Kind: "primary_key", Columns: []string{"id"}},
			{Kind: "foreign_key", Name: "shipping_user_fk", Columns: []string{"shipping_user_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}},
			{Kind: "foreign_key", Name: "billing_user_fk", Columns: []string{"billing_user_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}},
		}
		if reverse {
			constraints[1], constraints[2] = constraints[2], constraints[1]
		}
		return compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}, Objects: []compilerir.PhysicalObject{
			{ID: "tasks", Kind: "table", Name: "tasks", Columns: []compilerir.PhysicalColumn{{Name: "id", LogicalKind: "integer"}, {Name: "shipping_user_id", Ordinal: 1, LogicalKind: "integer"}, {Name: "billing_user_id", Ordinal: 2, LogicalKind: "integer"}}, Constraints: constraints},
			{ID: "users", Kind: "table", Name: "users", Columns: []compilerir.PhysicalColumn{{Name: "id", LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}}},
		}}
	}
	left, leftDiagnostics := compilerir.BuildSemantic(makeCatalog(false), compilerir.MappingConfig{}, nil)
	right, rightDiagnostics := compilerir.BuildSemantic(makeCatalog(true), compilerir.MappingConfig{}, nil)
	if len(leftDiagnostics) != 0 || len(rightDiagnostics) != 0 {
		t.Fatalf("reordered foreign keys produced diagnostics: left=%#v right=%#v", leftDiagnostics, rightDiagnostics)
	}
	if got, want := relationNames(relationObject(left, "tasks")), []string{"BillingUser", "ShippingUser"}; !equalStrings(got, want) {
		t.Fatalf("unexpected direct relation order: got=%v want=%v", got, want)
	}
	if got, want := relationNames(relationObject(left, "users")), []string{"BillingUserTasks", "ShippingUserTasks"}; !equalStrings(got, want) {
		t.Fatalf("unexpected inverse relation order: got=%v want=%v", got, want)
	}
	if !equalStrings(relationNames(relationObject(left, "tasks")), relationNames(relationObject(right, "tasks"))) || !equalStrings(relationNames(relationObject(left, "users")), relationNames(relationObject(right, "users"))) {
		t.Fatalf("relation order changed with physical constraint order: left=%#v right=%#v", left, right)
	}
}

func relationObject(model compilerir.SemanticModel, id compilerir.ObjectID) compilerir.SemanticObject {
	for _, object := range model.Objects {
		if object.ID == id {
			return object
		}
	}
	return compilerir.SemanticObject{}
}

func relation(object compilerir.SemanticObject, name string) compilerir.SemanticRelation {
	for _, relation := range object.Relations {
		if relation.Name == name {
			return relation
		}
	}
	return compilerir.SemanticRelation{}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func relationNames(object compilerir.SemanticObject) []string {
	names := make([]string, 0, len(object.Relations))
	for _, relation := range object.Relations {
		names = append(names, relation.Name)
	}
	return names
}

func objectIDName(id compilerir.ObjectID) string {
	if id == "members" {
		return "assignee"
	}
	return "project"
}
