package changeplan

import (
	"os"
	"testing"

	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

type immutabilityProfileSource struct {
	value           engineprofile.Profile
	calls           [5]int
	mutateAfterLast bool
}

func (s *immutabilityProfileSource) ID() string             { s.calls[0]++; return s.value.ID }
func (s *immutabilityProfileSource) Engine() EngineID       { s.calls[1]++; return s.value.Engine }
func (s *immutabilityProfileSource) Version() EngineVersion { s.calls[2]++; return s.value.Version }
func (s *immutabilityProfileSource) Capabilities() EngineCapabilities {
	s.calls[3]++
	return s.value.Capabilities
}
func (s *immutabilityProfileSource) Limits() EngineLimits {
	s.calls[4]++
	value := s.value.Limits
	if s.mutateAfterLast {
		s.value.ID = "mutated-source"
		s.value.Engine = engineprofile.Custom
		s.value.Version = engineprofile.Version{}
		s.value.Capabilities = engineprofile.Capabilities{}
		s.value.Limits.MaxBindParameters = 1
	}
	return value
}

func immutabilityProfileValue(t *testing.T) engineprofile.Profile {
	t.Helper()
	value, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 35})
	require.NoError(t, err)
	return value
}

func immutabilitySource(t *testing.T, mutateAfterLast bool) *immutabilityProfileSource {
	t.Helper()
	return &immutabilityProfileSource{value: immutabilityProfileValue(t), mutateAfterLast: mutateAfterLast}
}

func TestImmutabilityMatrixProfileBoundaries(t *testing.T) {
	value := immutabilityProfileValue(t)
	want, err := NewProfile(immutabilitySource(t, false))
	require.NoError(t, err)
	wantDigest, err := ProfileDigest(&immutabilityProfileSource{value: value})
	require.NoError(t, err)

	for _, row := range []struct {
		name   string
		mutate func(*engineprofile.Profile)
	}{
		{"source ID", func(v *engineprofile.Profile) { v.ID = "changed" }},
		{"source engine", func(v *engineprofile.Profile) { v.Engine = engineprofile.Custom }},
		{"source version", func(v *engineprofile.Profile) { v.Version.Minor++ }},
		{"source capability", func(v *engineprofile.Profile) { v.Capabilities.Savepoints = !v.Capabilities.Savepoints }},
		{"source bind limit", func(v *engineprofile.Profile) { v.Limits.MaxBindParameters++ }},
	} {
		row := row
		t.Run("NewProfile input "+row.name, func(t *testing.T) {
			source := immutabilitySource(t, false)
			profile, profileErr := NewProfile(source)
			require.NoError(t, profileErr)
			before := profile
			row.mutate(&source.value)
			require.Equal(t, before.ID(), profile.ID())
			require.Equal(t, before.Engine(), profile.Engine())
			require.Equal(t, before.Version(), profile.Version())
			require.Equal(t, before.Capabilities(), profile.Capabilities())
			require.Equal(t, before.Limits(), profile.Limits())
			require.Equal(t, []int{1, 1, 1, 1, 1}, source.calls[:])
		})
	}

	t.Run("Profile accessor values", func(t *testing.T) {
		version := want.Version()
		version.Minor++
		caps := want.Capabilities()
		caps.Savepoints = !caps.Savepoints
		limits := want.Limits()
		limits.MaxBindParameters++
		require.Equal(t, value.Version, want.Version())
		require.Equal(t, value.Capabilities, want.Capabilities())
		require.Equal(t, value.Limits, want.Limits())
		require.Equal(t, wantDigest, mustProfileDigest(t, want))
	})

	t.Run("ProfileDigest mutating source", func(t *testing.T) {
		source := immutabilitySource(t, true)
		got, digestErr := ProfileDigest(source)
		require.NoError(t, digestErr)
		require.Equal(t, wantDigest, got)
		require.Equal(t, []int{1, 1, 1, 1, 1}, source.calls[:])
	})

	t.Run("NewPlan ProfileSource input", func(t *testing.T) {
		plan, baseline, history, operation := immutabilityPlanParts(t)
		source := immutabilitySource(t, true)
		built, buildErr := NewPlan(source, baseline, history, plan.Decisions(), []Operation{operation})
		require.NoError(t, buildErr)
		require.Equal(t, plan.ID(), built.ID())
		require.Equal(t, []int{1, 1, 1, 1, 1}, source.calls[:])
		require.Equal(t, plan.Profile().ID(), built.Profile().ID())
		encoded, encodeErr := Encode(built)
		require.NoError(t, encodeErr)
		require.Equal(t, mustEncodePlan(t, plan), encoded)
	})

	t.Run("NewCatalog ProfileSource input", func(t *testing.T) {
		source := immutabilitySource(t, true)
		object := immutabilityObject(t, "catalog-profile")
		catalog, catalogErr := NewCatalog(source, "profile-source", []CatalogObject{object})
		require.NoError(t, catalogErr)
		require.Equal(t, "profile-source", catalog.SourceIdentity())
		got, ok := catalog.ObjectID(schema.ObjectTable, "main", "users")
		require.True(t, ok)
		require.Equal(t, object.ID(), got)
		expected, err := NewCatalog(sourceRepairProfile(t), "profile-source", []CatalogObject{object})
		require.NoError(t, err)
		require.Equal(t, mustCatalogDigest(t, expected), mustCatalogDigest(t, catalog))
		require.Equal(t, []int{1, 1, 1, 1, 1}, source.calls[:])
	})

	t.Run("FromLock ProfileSource input", func(t *testing.T) {
		lock, readErr := os.ReadFile("testdata/external/lock.json")
		require.NoError(t, readErr)
		lockCatalog, catalogErr := CatalogFromLock(lock)
		require.NoError(t, catalogErr)
		resolved, resolvedErr := NewResolvedChanges(lockCatalog, []ResolvedCatalogStep{}, nil, nil, nil, nil)
		require.NoError(t, resolvedErr)
		history := mustHistory(t)
		profileValue, profileErr := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 45})
		require.NoError(t, profileErr)
		source := &immutabilityProfileSource{value: profileValue, mutateAfterLast: true}
		plan, buildErr := FromLock(lock, source, history, resolved)
		require.NoError(t, buildErr)
		beforeID := plan.ID()
		beforeBytes := mustEncodePlan(t, plan)
		for i := range lock {
			lock[i] = 'x'
		}
		require.Equal(t, beforeID, plan.ID())
		require.Equal(t, beforeBytes, mustEncodePlan(t, plan))
		require.Equal(t, []int{1, 1, 1, 1, 1}, source.calls[:])
	})
}

func mustProfileDigest(t *testing.T, profile Profile) Digest {
	t.Helper()
	value, err := ProfileDigest(profileSourceValue{profile: profile})
	require.NoError(t, err)
	return value
}

type profileSourceValue struct{ profile Profile }

func (s profileSourceValue) ID() string                       { return s.profile.ID() }
func (s profileSourceValue) Engine() EngineID                 { return s.profile.Engine() }
func (s profileSourceValue) Version() EngineVersion           { return s.profile.Version() }
func (s profileSourceValue) Capabilities() EngineCapabilities { return s.profile.Capabilities() }
func (s profileSourceValue) Limits() EngineLimits             { return s.profile.Limits() }

func immutabilityDefinition() schema.TableDef {
	return schema.TableDef{
		Schema: "main", Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}, GoBinding: &schema.GoBinding{Type: "int64", Imports: []schema.GoImport{{Path: "database/sql"}}}, NativeType: &schema.NativeTypeDef{Dialect: "sqlite", Schema: "main", Name: "integer", Kind: schema.NativeArray, Arguments: []string{"signed"}, Element: &schema.NativeTypeDef{Dialect: "sqlite", Name: "element", Kind: schema.NativeBuiltin}}},
			{Name: "name", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{
			{Name: "users_name_key", Columns: []string{"name"}, IncludeColumns: []string{"id"}, StorageParameters: map[string]string{"fillfactor": "70"}, Collations: map[string]string{"name": "BINARY"}},
			{Name: "users_order_key", Keys: []schema.IndexKeyDef{{Expression: "id"}}},
		},
		Checks:               []schema.CheckDef{{Name: "users_id_check", Expression: "id > 0"}},
		ExclusionConstraints: []schema.ExclusionDef{{Name: "users_exclusion", Method: "gist", Elements: []schema.ExclusionElementDef{{Expression: "id", Operator: "="}}}},
		Indexes: []schema.IndexDef{
			{Name: "users_name_idx", Columns: []string{"name"}, IncludeColumns: []string{"id"}, StorageParameters: map[string]string{"fillfactor": "80"}},
			{Name: "users_expr_idx", Expressions: []sqltext.Text{"lower(name)"}},
			{Name: "users_key_idx", Keys: []schema.IndexKeyDef{{Expression: "name", Descending: true}}},
		},
		ForeignKeys:   []schema.ForeignKeyDef{{Name: "users_parent", Columns: []string{"id"}, ReferencedSchema: "main", ReferencedTable: "users", ReferencedColumns: []string{"id"}, OnDelete: schema.SetNull, DeleteSetColumns: []string{"id"}}},
		Relationships: []schema.RelationshipDef{{Name: "parent", Kind: schema.RelationshipBelongsTo, Columns: []string{"id"}, ReferencedSchema: "main", ReferencedTable: "users", ReferencedColumns: []string{"id"}}},
	}
}

func immutabilityObject(t *testing.T, id ObjectID) CatalogObject {
	t.Helper()
	object, err := NewCatalogObject(id, immutabilityDefinition())
	require.NoError(t, err)
	return object
}

func TestImmutabilityMatrixCatalogObjectDefinitions(t *testing.T) {
	rows := []struct {
		name    string
		mutate  func(*schema.TableDef)
		prepare func(*schema.TableDef)
	}{
		{"columns slice", func(d *schema.TableDef) { d.Columns[0].Name = "changed_column" }, nil},
		{"GoBinding pointer", func(d *schema.TableDef) { d.Columns[0].GoBinding.Type = "string" }, nil},
		{"GoBinding imports slice", func(d *schema.TableDef) { d.Columns[0].GoBinding.Imports[0].Path = "changed/import" }, nil},
		{"NativeType pointer", func(d *schema.TableDef) { d.Columns[0].NativeType.Name = "changed_native" }, nil},
		{"Native arguments slice", func(d *schema.TableDef) { d.Columns[0].NativeType.Arguments[0] = "changed_arg" }, nil},
		{"nested NativeType element", func(d *schema.TableDef) { d.Columns[0].NativeType.Element.Name = "changed_element" }, nil},
		{"primary key slice", func(d *schema.TableDef) { d.PrimaryKey[0] = "name" }, nil},
		{"Unique constraints slice", func(d *schema.TableDef) { d.UniqueConstraints[0].Name = "changed_unique" }, nil},
		{"Unique columns slice", func(d *schema.TableDef) { d.UniqueConstraints[0].Columns[0] = "id" }, nil},
		{"Unique include slice", func(d *schema.TableDef) { d.UniqueConstraints[0].IncludeColumns[0] = "name" }, nil},
		{"Unique storage map", func(d *schema.TableDef) { d.UniqueConstraints[0].StorageParameters["fillfactor"] = "71" }, nil},
		{"Unique collation map", func(d *schema.TableDef) { d.UniqueConstraints[0].Collations["name"] = "NOCASE" }, nil},
		{"Unique key slice", func(d *schema.TableDef) { d.UniqueConstraints[1].Keys[0].Expression = "name" }, nil},
		{"Checks slice", func(d *schema.TableDef) { d.Checks[0].Expression = "id >= 0" }, nil},
		{"Exclusions slice", func(d *schema.TableDef) { d.ExclusionConstraints[0].Name = "changed_exclusion" }, nil},
		{"Exclusion elements", func(d *schema.TableDef) { d.ExclusionConstraints[0].Elements[0].Operator = "&&" }, nil},
		{"Indexes slice", func(d *schema.TableDef) { d.Indexes[0].Name = "changed_index" }, nil},
		{"Index columns slice", func(d *schema.TableDef) { d.Indexes[0].Columns[0] = "id" }, nil},
		{"Index expressions slice", func(d *schema.TableDef) { d.Indexes[1].Expressions[0] = "upper(name)" }, nil},
		{"Index include slice", func(d *schema.TableDef) { d.Indexes[0].IncludeColumns[0] = "name" }, nil},
		{"Index keys slice", func(d *schema.TableDef) { d.Indexes[2].Keys[0].Expression = "id" }, nil},
		{"Index storage map", func(d *schema.TableDef) { d.Indexes[0].StorageParameters["fillfactor"] = "81" }, nil},
		{"Foreign keys slice", func(d *schema.TableDef) { d.ForeignKeys[0].Name = "changed_fk" }, nil},
		{"FK local columns", func(d *schema.TableDef) { d.ForeignKeys[0].Columns[0] = "name" }, nil},
		{"FK referenced columns", func(d *schema.TableDef) { d.ForeignKeys[0].ReferencedColumns[0] = "name" }, nil},
		{"FK delete-set columns", func(d *schema.TableDef) { d.ForeignKeys[0].DeleteSetColumns[0] = "name" }, nil},
		{"Relationships slice", func(d *schema.TableDef) { d.Relationships[0].Name = "changed_relationship" }, nil},
		{"Relationship columns", func(d *schema.TableDef) { d.Relationships[0].Columns[0] = "name" }, nil},
		{"Relationship referenced columns", func(d *schema.TableDef) { d.Relationships[0].ReferencedColumns[0] = "name" }, nil},
		{"Relationship through pointer", func(d *schema.TableDef) {
			d.Relationships[0].Through.Table.Name = "changed_join"
		}, func(d *schema.TableDef) {
			d.Relationships[0].Kind = schema.RelationshipManyToMany
			d.Relationships[0].Through = &schema.RelationshipThrough{Table: schema.ObjectName{Schema: "main", Name: "join"}, SourceColumns: []string{"id"}, TargetColumns: []string{"id"}}
		}},
		{"Relationship through source slice", func(d *schema.TableDef) {
			d.Relationships[0].Through.SourceColumns[0] = "name"
		}, func(d *schema.TableDef) {
			d.Relationships[0].Kind = schema.RelationshipManyToMany
			d.Relationships[0].Through = &schema.RelationshipThrough{Table: schema.ObjectName{Schema: "main", Name: "join"}, SourceColumns: []string{"id"}, TargetColumns: []string{"id"}}
		}},
		{"Relationship through target slice", func(d *schema.TableDef) {
			d.Relationships[0].Through.TargetColumns[0] = "name"
		}, func(d *schema.TableDef) {
			d.Relationships[0].Kind = schema.RelationshipManyToMany
			d.Relationships[0].Through = &schema.RelationshipThrough{Table: schema.ObjectName{Schema: "main", Name: "join"}, SourceColumns: []string{"id"}, TargetColumns: []string{"id"}}
		}},
	}
	build := func(t *testing.T, prepare func(*schema.TableDef)) (CatalogObject, schema.TableDef, Digest, schema.TableDef) {
		t.Helper()
		definition := immutabilityDefinition()
		if prepare != nil {
			prepare(&definition)
		}
		wantDefinition := definition.Clone()
		object, err := NewCatalogObject("rich-object", definition)
		require.NoError(t, err)
		catalog, err := NewCatalog(sourceRepairProfile(t), "rich-source", []CatalogObject{object})
		require.NoError(t, err)
		wantDigest, err := CatalogDigest(catalog)
		require.NoError(t, err)
		return object, definition, wantDigest, wantDefinition
	}
	for _, row := range rows {
		row := row
		t.Run("NewCatalogObject definition input "+row.name, func(t *testing.T) {
			object, definition, wantDigest, wantDefinition := build(t, row.prepare)
			row.mutate(&definition)
			require.Equal(t, wantDefinition, object.Definition())
			fresh, err := NewCatalog(sourceRepairProfile(t), "rich-source", []CatalogObject{object})
			require.NoError(t, err)
			require.Equal(t, wantDigest, mustCatalogDigest(t, fresh))
		})
		t.Run("CatalogObject.Definition return "+row.name, func(t *testing.T) {
			object, _, wantDigest, wantDefinition := build(t, row.prepare)
			stored := object.Definition()
			row.mutate(&stored)
			require.Equal(t, wantDefinition, object.Definition())
			fresh, err := NewCatalog(sourceRepairProfile(t), "rich-source", []CatalogObject{object})
			require.NoError(t, err)
			require.Equal(t, wantDigest, mustCatalogDigest(t, fresh))
		})
	}

	t.Run("NewCatalogObject virtual module arguments input", func(t *testing.T) {
		definition := schema.TableDef{Schema: "main", Name: "docs", VirtualTableModule: "fts5", VirtualTableModuleArguments: []string{"content=body"}, Columns: []schema.ColumnDef{{Name: "body", Type: schema.TextType{}}}}
		object, err := NewCatalogObject("virtual-object", definition)
		require.NoError(t, err)
		definition.VirtualTableModuleArguments[0] = "content=changed"
		require.Equal(t, "content=body", object.Definition().VirtualTableModuleArguments[0])
	})
	t.Run("CatalogObject.Definition virtual module arguments return", func(t *testing.T) {
		definition := schema.TableDef{Schema: "main", Name: "docs", VirtualTableModule: "fts5", VirtualTableModuleArguments: []string{"content=body"}, Columns: []schema.ColumnDef{{Name: "body", Type: schema.TextType{}}}}
		object, err := NewCatalogObject("virtual-object", definition)
		require.NoError(t, err)
		returned := object.Definition()
		returned.VirtualTableModuleArguments[0] = "content=returned"
		require.Equal(t, "content=body", object.Definition().VirtualTableModuleArguments[0])
	})

	t.Run("NewIntroducedBaselineObject input", func(t *testing.T) {
		definition := immutabilityDefinition()
		object, err := NewIntroducedBaselineObject("source", "create", definition)
		require.NoError(t, err)
		wantID, wantKind, wantSchema, wantName := object.ID(), object.Kind(), object.Schema(), object.Name()
		definition.Name, definition.Schema, definition.Kind = "changed", "other", schema.ObjectView
		definition.Columns[0].Name = "changed_column"
		require.Equal(t, wantID, object.ID())
		require.Equal(t, wantKind, object.Kind())
		require.Equal(t, wantSchema, object.Schema())
		require.Equal(t, wantName, object.Name())
		require.Equal(t, OperationID("create"), object.IntroducedBy())
	})
}

func mustCatalogDigest(t *testing.T, catalog Catalog) Digest {
	t.Helper()
	digest, err := CatalogDigest(catalog)
	require.NoError(t, err)
	return digest
}

func assertCatalogIdentityAndLookup(t *testing.T, catalog Catalog, source string, objectID ObjectID) {
	t.Helper()
	require.Equal(t, source, catalog.SourceIdentity())
	got, ok := catalog.ObjectID(schema.ObjectTable, "main", "users")
	require.True(t, ok)
	require.Equal(t, objectID, got)
}

func immutabilityPlanParts(t *testing.T) (Plan, BaselineIdentity, HistoryIdentity, Operation) {
	t.Helper()
	profile := sourceRepairProfile(t)
	object := immutabilityObject(t, "starting")
	catalog, err := NewCatalog(profile, "plan-source", []CatalogObject{object})
	require.NoError(t, err)
	profileDigest, err := ProfileDigest(profile)
	require.NoError(t, err)
	catalogDigest, err := CatalogDigest(catalog)
	require.NoError(t, err)
	identity, err := NewCatalogIdentity(profile.Engine(), profileDigest, catalogDigest, Digest{3})
	require.NoError(t, err)
	baselineObject, err := NewBaselineObject(object.ID(), "table", "main", "users")
	require.NoError(t, err)
	baseline, err := NewBaselineIdentity(identity, catalog.SourceIdentity(), []BaselineObject{baselineObject}, nil)
	require.NoError(t, err)
	history := mustHistory(t)
	args := []any{[]byte("payload"), int64(1), "name"}
	operation, err := NewOperation("operation", OperationNativeSQL, nil, []ObjectID{object.ID()}, nil, nil, Digest{1}, []stmt.Statement{stmt.New(sqltext.Text("SELECT ?"), args...)}, TransactionRequired, false, nil)
	require.NoError(t, err)
	decision, err := NewDecision("operation-decision", DecisionAcceptNativeSQL, object.ID(), "", "", true, "approved")
	require.NoError(t, err)
	plan, err := NewPlan(profile, baseline, history, []Decision{decision}, []Operation{operation})
	require.NoError(t, err)
	return plan, baseline, history, operation
}

func mustEncodePlan(t *testing.T, plan Plan) []byte {
	t.Helper()
	encoded, err := Encode(plan)
	require.NoError(t, err)
	return encoded
}

func immutabilityResolvedFixture(t *testing.T) (ResolvedChanges, Catalog, Catalog, Operation, Decision) {
	t.Helper()
	profile := sourceRepairProfile(t)
	object := immutabilityObject(t, "starting")
	baseline, err := NewCatalog(profile, "resolved-source", []CatalogObject{object})
	require.NoError(t, err)
	after, err := NewCatalog(profile, "resolved-source", []CatalogObject{object})
	require.NoError(t, err)
	digest := mustCatalogDigest(t, after)
	operation, err := NewOperation("operation", OperationNativeSQL, nil, []ObjectID{object.ID()}, nil, nil, digest,
		[]stmt.Statement{stmt.New(sqltext.Text("SELECT ?"), []byte("payload"), int64(1))}, TransactionRequired, false, nil)
	require.NoError(t, err)
	decision, err := NewDecision("operation-decision", DecisionAcceptNativeSQL, object.ID(), "", "", true, "approved")
	require.NoError(t, err)
	step, err := NewResolvedCatalogStep(operation.ID(), after)
	require.NoError(t, err)
	resolved, err := NewResolvedChanges(baseline, []ResolvedCatalogStep{step}, []Decision{decision}, []Operation{operation}, nil, nil)
	require.NoError(t, err)
	return resolved, baseline, after, operation, decision
}

func TestImmutabilityMatrixCatalogBoundaries(t *testing.T) {
	t.Run("NewCatalog objects input", func(t *testing.T) {
		object := immutabilityObject(t, "starting")
		objects := []CatalogObject{object}
		catalog, err := NewCatalog(sourceRepairProfile(t), "objects-source", objects)
		require.NoError(t, err)
		want := mustCatalogDigest(t, catalog)
		objects[0] = immutabilityObject(t, "replaced")
		require.Len(t, append(objects, immutabilityObject(t, "appended")), 2)
		require.Equal(t, want, mustCatalogDigest(t, catalog))
		got, ok := catalog.ObjectID(schema.ObjectTable, "main", "users")
		require.True(t, ok)
		require.Equal(t, object.ID(), got)
	})

	t.Run("NewCatalogLike objects input", func(t *testing.T) {
		basisObject := immutabilityObject(t, "basis")
		basis, err := NewCatalog(sourceRepairProfile(t), "like-source", []CatalogObject{basisObject})
		require.NoError(t, err)
		definition := immutabilityDefinition()
		object, err := NewCatalogObject("starting", definition)
		require.NoError(t, err)
		objects := []CatalogObject{object}
		catalog, err := NewCatalogLike(basis, objects)
		require.NoError(t, err)
		want := mustCatalogDigest(t, catalog)
		objects[0] = immutabilityObject(t, "replaced")
		definition.Columns[0].Name = "changed-original-definition"
		require.Equal(t, want, mustCatalogDigest(t, catalog))
		require.Equal(t, "like-source", catalog.SourceIdentity())
		got, ok := catalog.ObjectID(schema.ObjectTable, "main", "users")
		require.True(t, ok)
		require.Equal(t, object.ID(), got)
	})

	t.Run("CatalogFromLock bytes input", func(t *testing.T) {
		lock, err := os.ReadFile("testdata/external/lock.json")
		require.NoError(t, err)
		catalog, err := CatalogFromLock(lock)
		require.NoError(t, err)
		wantDigest := mustCatalogDigest(t, catalog)
		wantID, ok := catalog.ObjectID(schema.ObjectTable, "main", "tasks")
		require.True(t, ok)
		for i := range lock {
			lock[i] = 'x'
		}
		require.Equal(t, wantDigest, mustCatalogDigest(t, catalog))
		gotID, ok := catalog.ObjectID(schema.ObjectTable, "main", "tasks")
		require.True(t, ok)
		require.Equal(t, wantID, gotID)
	})

	t.Run("NewResolvedCatalogStep Catalog input", func(t *testing.T) {
		_, baseline, _, operation, _ := immutabilityResolvedFixture(t)
		step, err := NewResolvedCatalogStep(operation.ID(), baseline)
		require.NoError(t, err)
		want := mustCatalogDigest(t, step.Catalog())
		baseline.sourceIdentity = "changed-source"
		baseline.physical.Engine.Dialect = "changed-engine"
		baseline.physical.Objects[0].ID = "changed-id"
		baseline.physical.Objects[0].Columns[0].Name = "changed-column"
		require.Equal(t, want, mustCatalogDigest(t, step.Catalog()))
		assertCatalogIdentityAndLookup(t, step.Catalog(), "resolved-source", "starting")
		require.Equal(t, operation.ID(), step.Operation())
	})

	t.Run("ResolvedCatalogStep.Catalog return", func(t *testing.T) {
		_, baseline, _, operation, _ := immutabilityResolvedFixture(t)
		step, err := NewResolvedCatalogStep(operation.ID(), baseline)
		require.NoError(t, err)
		want := mustCatalogDigest(t, step.Catalog())
		returned := step.Catalog()
		returned.sourceIdentity = "changed-source"
		returned.physical.Engine.Dialect = "changed-engine"
		returned.physical.Objects[0].ID = "changed-id"
		returned.physical.Objects[0].Columns[0].Name = "changed-column"
		require.Equal(t, want, mustCatalogDigest(t, step.Catalog()))
		assertCatalogIdentityAndLookup(t, step.Catalog(), "resolved-source", "starting")
	})

	t.Run("NewResolvedChanges baseline input", func(t *testing.T) {
		resolved, baseline, _, _, _ := immutabilityResolvedFixture(t)
		wantBaseline := mustCatalogDigest(t, resolved.BaselineCatalog())
		wantTarget := mustCatalogDigest(t, resolved.TargetCatalog())
		baseline.sourceIdentity = "changed-source"
		baseline.physical.Engine.Dialect = "changed-engine"
		baseline.physical.Objects[0].ID = "changed-id"
		baseline.physical.Objects[0].Columns[0].Name = "changed-column"
		require.Equal(t, wantBaseline, mustCatalogDigest(t, resolved.BaselineCatalog()))
		require.Equal(t, wantTarget, mustCatalogDigest(t, resolved.TargetCatalog()))
		assertCatalogIdentityAndLookup(t, resolved.BaselineCatalog(), "resolved-source", "starting")
		assertCatalogIdentityAndLookup(t, resolved.TargetCatalog(), "resolved-source", "starting")
	})

	t.Run("NewResolvedChanges steps input", func(t *testing.T) {
		resolved, baseline, after, operation, _ := immutabilityResolvedFixture(t)
		step, err := NewResolvedCatalogStep(operation.ID(), after)
		require.NoError(t, err)
		steps := []ResolvedCatalogStep{step}
		fresh, err := NewResolvedChanges(baseline, steps, resolved.Decisions(), resolved.Operations(), nil, nil)
		require.NoError(t, err)
		want := mustCatalogDigest(t, fresh.CatalogSteps()[0].Catalog())
		steps[0] = ResolvedCatalogStep{operation: "changed", after: baseline}
		step.after.physical.Objects[0].Columns[0].Name = "changed-column"
		// This strict assertion is the local regression for NewResolvedChanges aliasing step Catalog values.
		require.Equal(t, want, mustCatalogDigest(t, fresh.CatalogSteps()[0].Catalog()))
		require.Equal(t, operation.ID(), fresh.CatalogSteps()[0].Operation())
	})

	for _, row := range []struct {
		name   string
		mutate func(Catalog)
	}{
		{"ResolvedChanges.BaselineCatalog", func(c Catalog) {
			c.sourceIdentity = "changed"
			c.physical.Engine.Dialect = "other"
			c.physical.Objects[0].ID = "other"
			c.physical.Objects[0].Columns[0].Name = "other"
		}},
		{"ResolvedChanges.TargetCatalog", func(c Catalog) {
			c.sourceIdentity = "changed"
			c.physical.Engine.Dialect = "other"
			c.physical.Objects[0].ID = "other"
			c.physical.Objects[0].Columns[0].Name = "other"
		}},
	} {
		row := row
		t.Run(row.name+" return", func(t *testing.T) {
			resolved, _, _, _, _ := immutabilityResolvedFixture(t)
			beforeBase := mustCatalogDigest(t, resolved.BaselineCatalog())
			beforeTarget := mustCatalogDigest(t, resolved.TargetCatalog())
			row.mutate(func() Catalog {
				if row.name == "ResolvedChanges.BaselineCatalog" {
					return resolved.BaselineCatalog()
				}
				return resolved.TargetCatalog()
			}())
			require.Equal(t, beforeBase, mustCatalogDigest(t, resolved.BaselineCatalog()))
			require.Equal(t, beforeTarget, mustCatalogDigest(t, resolved.TargetCatalog()))
		})
	}

	t.Run("ResolvedChanges.CatalogSteps slice return", func(t *testing.T) {
		resolved, _, after, _, _ := immutabilityResolvedFixture(t)
		want := mustCatalogDigest(t, resolved.CatalogSteps()[0].Catalog())
		steps := resolved.CatalogSteps()
		returnedStep := steps[0]
		steps[0] = ResolvedCatalogStep{operation: "changed", after: after}
		returnedStep.after.physical.Objects[0].ID = "changed"
		require.Len(t, append(steps, returnedStep), 2)
		got := resolved.CatalogSteps()
		require.Len(t, got, 1)
		require.Equal(t, OperationID("operation"), got[0].Operation())
		require.Equal(t, want, mustCatalogDigest(t, got[0].Catalog()))
		assertCatalogIdentityAndLookup(t, got[0].Catalog(), "resolved-source", "starting")
		require.Equal(t, want, mustCatalogDigest(t, resolved.TargetCatalog()))
	})
}

func immutabilityFutureFixture(t *testing.T) (ResolvedChanges, []BaselineObject, []BaselineRename) {
	t.Helper()
	profile := sourceRepairProfile(t)
	baseline, err := NewCatalog(profile, "future-source", []CatalogObject{})
	require.NoError(t, err)
	definition := schema.TableDef{Schema: "main", Name: "new_table", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}}
	future, err := NewIntroducedBaselineObject("future-source", "create", definition)
	require.NoError(t, err)
	created, err := NewCatalogObject(future.ID(), definition)
	require.NoError(t, err)
	after, err := NewCatalog(profile, "future-source", []CatalogObject{created})
	require.NoError(t, err)
	digest := mustCatalogDigest(t, after)
	operation, err := NewOperation("create", OperationCreateTable, nil, []ObjectID{future.ID()}, nil, nil, digest,
		[]stmt.Statement{stmt.New(sqltext.Text("CREATE TABLE new_table (id INTEGER)"))}, TransactionRequired, false, nil)
	require.NoError(t, err)
	step, err := NewResolvedCatalogStep(operation.ID(), after)
	require.NoError(t, err)
	resolved, err := NewResolvedChanges(baseline, []ResolvedCatalogStep{step}, nil, []Operation{operation}, []BaselineObject{future}, nil)
	require.NoError(t, err)
	return resolved, []BaselineObject{future}, nil
}

func TestImmutabilityMatrixBaselineBoundaries(t *testing.T) {
	profile := sourceRepairProfile(t)
	catalog, err := NewCatalog(profile, "baseline-source", []CatalogObject{immutabilityObject(t, "starting")})
	require.NoError(t, err)
	profileDigest, err := ProfileDigest(profile)
	require.NoError(t, err)
	catalogDigest := mustCatalogDigest(t, catalog)
	identity, err := NewCatalogIdentity(profile.Engine(), profileDigest, catalogDigest, Digest{3})
	require.NoError(t, err)
	object, err := NewBaselineObject("starting", "table", "main", "users")
	require.NoError(t, err)
	rename, err := NewBaselineRename("rename", object.ID(), "main", "accounts")
	require.NoError(t, err)

	t.Run("NewBaselineIdentity objects input", func(t *testing.T) {
		objects := []BaselineObject{object}
		baseline, baselineErr := NewBaselineIdentity(identity, "baseline-source", objects, []BaselineRename{rename})
		require.NoError(t, baselineErr)
		objects[0] = BaselineObject{}
		require.Len(t, append(objects, object), 2)
		require.Equal(t, object, baseline.Objects()[0])
		require.Equal(t, []BaselineRename{rename}, baseline.Renames())
	})
	t.Run("NewBaselineIdentity renames input", func(t *testing.T) {
		renamed := []BaselineRename{rename}
		baseline, baselineErr := NewBaselineIdentity(identity, "baseline-source", []BaselineObject{object}, renamed)
		require.NoError(t, baselineErr)
		renamed[0] = BaselineRename{}
		require.Len(t, append(renamed, rename), 2)
		require.Equal(t, []BaselineRename{rename}, baseline.Renames())
	})
	baseline, err := NewBaselineIdentity(identity, "baseline-source", []BaselineObject{object}, []BaselineRename{rename})
	require.NoError(t, err)
	t.Run("BaselineIdentity.Objects return", func(t *testing.T) {
		returned := baseline.Objects()
		returned[0] = BaselineObject{}
		require.Len(t, append(returned, object), 2)
		require.Equal(t, object, baseline.Objects()[0])
		require.Len(t, baseline.Objects(), 1)
	})
	t.Run("BaselineIdentity.Renames return", func(t *testing.T) {
		returned := baseline.Renames()
		returned[0] = BaselineRename{}
		require.Len(t, append(returned, rename), 2)
		require.Equal(t, []BaselineRename{rename}, baseline.Renames())
	})

	plan, planBaseline, history, operation := immutabilityPlanParts(t)
	t.Run("Plan.Baseline return", func(t *testing.T) {
		beforeBytes := mustEncodePlan(t, plan)
		returned := plan.Baseline()
		returned.objects[0] = BaselineObject{}
		returned.renames = append(returned.renames, rename)
		returned.sourceIdentity = "changed"
		// This strict equality also checks that the empty rename slice keeps its initialized value state.
		require.Equal(t, planBaseline, plan.Baseline())
		require.Equal(t, beforeBytes, mustEncodePlan(t, plan))
	})
	_ = history
	_ = operation

	t.Run("ResolvedChanges.FutureObjects input", func(t *testing.T) {
		resolved, futures, _ := immutabilityFutureFixture(t)
		want := resolved.FutureObjects()
		wantTarget := mustCatalogDigest(t, resolved.TargetCatalog())
		futures[0] = BaselineObject{}
		require.Equal(t, want, resolved.FutureObjects())
		require.Equal(t, wantTarget, mustCatalogDigest(t, resolved.TargetCatalog()))
	})
	t.Run("ResolvedChanges.FutureObjects return", func(t *testing.T) {
		resolved, _, _ := immutabilityFutureFixture(t)
		returned := resolved.FutureObjects()
		wantTarget := mustCatalogDigest(t, resolved.TargetCatalog())
		returned[0] = BaselineObject{}
		require.Len(t, append(returned, resolved.FutureObjects()[0]), 2)
		require.Equal(t, "new_table", resolved.FutureObjects()[0].Name())
		require.Equal(t, wantTarget, mustCatalogDigest(t, resolved.TargetCatalog()))
	})
}

func TestImmutabilityMatrixDecisionBoundaries(t *testing.T) {
	resolved, _, _, _, decision := immutabilityResolvedFixture(t)
	t.Run("NewResolvedChanges decisions input", func(t *testing.T) {
		decisions := []Decision{decision}
		fresh, err := NewResolvedChanges(resolved.BaselineCatalog(), resolved.CatalogSteps(), decisions, resolved.Operations(), nil, nil)
		require.NoError(t, err)
		decisions[0] = Decision{}
		require.Len(t, append(decisions, decision), 2)
		require.Equal(t, []Decision{decision}, fresh.Decisions())
	})
	t.Run("ResolvedChanges.Decisions return", func(t *testing.T) {
		returned := resolved.Decisions()
		returned[0] = Decision{}
		require.Len(t, append(returned, decision), 2)
		require.Equal(t, []Decision{decision}, resolved.Decisions())
	})

	plan, baseline, history, operation := immutabilityPlanParts(t)
	t.Run("NewPlan decisions input", func(t *testing.T) {
		planDecisions := plan.Decisions()
		built, err := NewPlan(plan.Profile(), baseline, history, planDecisions, []Operation{operation})
		require.NoError(t, err)
		planDecisions[0] = Decision{}
		require.Len(t, append(planDecisions, decision), 2)
		require.Equal(t, plan.Decisions(), built.Decisions())
	})
	t.Run("Plan.Decisions return", func(t *testing.T) {
		returned := plan.Decisions()
		returned[0] = Decision{}
		require.Len(t, append(returned, decision), 2)
		require.Equal(t, plan.Decisions(), plan.Decisions())
	})
	t.Run("Decision accessors unchanged", func(t *testing.T) {
		got := plan.Decisions()[0]
		require.Equal(t, decision.ID(), got.ID())
		require.Equal(t, decision.Kind(), got.Kind())
		require.Equal(t, decision.Object(), got.Object())
		require.Equal(t, decision.From(), got.From())
		require.Equal(t, decision.To(), got.To())
		require.Equal(t, decision.Accepted(), got.Accepted())
		require.Equal(t, decision.Reason(), got.Reason())
	})
}

func immutabilityOperation(t *testing.T, reversible bool) Operation {
	t.Helper()
	fact, err := NewFact("starting", "/name", FactOperatorEqual, `"users"`)
	require.NoError(t, err)
	args := []any{[]byte("payload"), int64(7)}
	reverse := []stmt.Statement(nil)
	if reversible {
		reverse = []stmt.Statement{stmt.New(sqltext.Text("SELECT ?"), []byte("reverse"))}
	}
	operation, err := NewOperation("operation", OperationNativeSQL, []OperationID{"dependency"}, []ObjectID{"starting"},
		[]Fact{fact}, []Fact{fact}, Digest{1}, []stmt.Statement{stmt.New(sqltext.Text("SELECT ?"), args...)},
		TransactionRequired, reversible, reverse)
	require.NoError(t, err)
	return operation
}

func assertOperationAccessors(t *testing.T, want, got Operation) {
	t.Helper()
	require.Equal(t, want.ID(), got.ID())
	require.Equal(t, want.Kind(), got.Kind())
	require.Equal(t, want.DependsOn(), got.DependsOn())
	require.Equal(t, want.Objects(), got.Objects())
	require.Equal(t, want.Preconditions(), got.Preconditions())
	require.Equal(t, want.Postconditions(), got.Postconditions())
	require.Equal(t, want.ResultDigest(), got.ResultDigest())
	require.Equal(t, want.Transaction(), got.Transaction())
	require.Equal(t, want.Reversible(), got.Reversible())
	require.Equal(t, want.Statements()[0].SQL(), got.Statements()[0].SQL())
	require.Equal(t, want.Statements()[0].Args(), got.Statements()[0].Args())
	require.Equal(t, want.ReverseStatements(), got.ReverseStatements())
}

func TestImmutabilityMatrixOperationBoundaries(t *testing.T) {
	fact, err := NewFact("starting", "/name", FactOperatorEqual, `"users"`)
	require.NoError(t, err)

	t.Run("NewOperation dependency input", func(t *testing.T) {
		depends := []OperationID{"dependency"}
		op, opErr := NewOperation("dependency-op", OperationNativeSQL, depends, []ObjectID{"starting"}, nil, nil, Digest{1},
			[]stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, TransactionRequired, false, nil)
		require.NoError(t, opErr)
		depends[0] = "changed"
		require.Len(t, append(depends, "other"), 2)
		require.Equal(t, []OperationID{"dependency"}, op.DependsOn())
		require.Equal(t, Digest{1}, op.ResultDigest())
	})
	t.Run("NewOperation object input", func(t *testing.T) {
		objects := []ObjectID{"starting"}
		op, opErr := NewOperation("object-op", OperationNativeSQL, nil, objects, nil, nil, Digest{1},
			[]stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, TransactionRequired, false, nil)
		require.NoError(t, opErr)
		objects[0] = "changed"
		require.Len(t, append(objects, "other"), 2)
		require.Equal(t, []ObjectID{"starting"}, op.Objects())
		require.Equal(t, Digest{1}, op.ResultDigest())
	})
	for _, row := range []struct {
		name  string
		field func(Operation) []Fact
	}{
		{"Precondition input", func(o Operation) []Fact { return o.Preconditions() }},
		{"Postcondition input", func(o Operation) []Fact { return o.Postconditions() }},
	} {
		row := row
		t.Run("NewOperation "+row.name, func(t *testing.T) {
			facts := []Fact{fact}
			op, opErr := NewOperation("fact-op", OperationNativeSQL, nil, []ObjectID{"starting"}, facts, facts, Digest{1},
				[]stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, TransactionRequired, false, nil)
			require.NoError(t, opErr)
			facts[0] = Fact{}
			require.Len(t, append(facts, fact), 2)
			require.Equal(t, []Fact{fact}, row.field(op))
			require.Equal(t, Digest{1}, op.ResultDigest())
		})
	}

	for _, reversible := range []bool{false, true} {
		label := "forward"
		if reversible {
			label = "reverse"
		}
		t.Run("NewOperationStatements "+label+" input", func(t *testing.T) {
			payload := []byte("payload")
			args := []any{payload}
			statements := []stmt.Statement{stmt.New(sqltext.Text("SELECT ?"), args...)}
			copied, copyErr := NewOperationStatements(statements)
			require.NoError(t, copyErr)
			statements[0].BoundArgs()[0].([]byte)[0] = 'X'
			args[0].([]byte)[0] = 'Y'
			require.Equal(t, "payload", string(copied[0].Args()[0].([]byte)))
		})
	}

	for _, reversible := range []bool{false, true} {
		label := "forward"
		if reversible {
			label = "reverse"
		}
		t.Run("NewOperation statements input "+label, func(t *testing.T) {
			payload := []byte("payload")
			args := []any{payload}
			forward := []stmt.Statement{stmt.New(sqltext.Text("SELECT ?"), args...)}
			reverse := []stmt.Statement(nil)
			if reversible {
				reverse = []stmt.Statement{stmt.New(sqltext.Text("SELECT ?"), []byte("reverse"))}
			}
			op, opErr := NewOperation("statement-op", OperationNativeSQL, nil, []ObjectID{"starting"}, nil, nil, Digest{1}, forward, TransactionRequired, reversible, reverse)
			require.NoError(t, opErr)
			forward[0].BoundArgs()[0].([]byte)[0] = 'X'
			args[0].([]byte)[0] = 'Y'
			require.Equal(t, "payload", string(op.Statements()[0].Args()[0].([]byte)))
			if reversible {
				reverse[0].BoundArgs()[0].([]byte)[0] = 'X'
				require.Equal(t, "reverse", string(op.ReverseStatements()[0].Args()[0].([]byte)))
			}
		})
	}

	for _, reversible := range []bool{false, true} {
		label := "forward"
		if reversible {
			label = "reverse"
		}
		t.Run("Operation accessors "+label, func(t *testing.T) {
			op := immutabilityOperation(t, reversible)
			before := op
			depends := op.DependsOn()
			depends[0] = "changed"
			require.Len(t, append(depends, "other"), 2)
			objects := op.Objects()
			objects[0] = "changed"
			require.Len(t, append(objects, "other"), 2)
			preconditions := op.Preconditions()
			preconditions[0] = Fact{}
			require.Len(t, append(preconditions, fact), 2)
			postconditions := op.Postconditions()
			postconditions[0] = Fact{}
			require.Len(t, append(postconditions, fact), 2)
			statements := op.Statements()
			statements[0].BoundArgs()[0].([]byte)[0] = 'X'
			require.Len(t, append(statements, statements[0]), 2)
			if reversible {
				reverse := op.ReverseStatements()
				reverse[0].BoundArgs()[0].([]byte)[0] = 'X'
				require.Len(t, append(reverse, reverse[0]), 2)
			}
			assertOperationAccessors(t, before, op)
		})
	}

	t.Run("ResolvedChanges operations input", func(t *testing.T) {
		resolved, _, _, operation, _ := immutabilityResolvedFixture(t)
		operations := []Operation{operation}
		expected := cloneOperation(operation)
		fresh, freshErr := NewResolvedChanges(resolved.BaselineCatalog(), resolved.CatalogSteps(), resolved.Decisions(), operations, nil, nil)
		require.NoError(t, freshErr)
		caller := operations[0]
		operations[0] = Operation{}
		caller.dependsOn = []OperationID{"changed"}
		caller.objects[0] = "changed"
		caller.statements[0].BoundArgs()[0].([]byte)[0] = 'X'
		freshOperation := fresh.Operations()[0]
		assertOperationAccessors(t, expected, freshOperation)
		require.Equal(t, "payload", string(freshOperation.Statements()[0].Args()[0].([]byte)))
	})
	t.Run("ResolvedChanges operations return", func(t *testing.T) {
		resolved, _, _, operation, _ := immutabilityResolvedFixture(t)
		returned := resolved.Operations()
		caller := returned[0]
		returned[0] = Operation{}
		caller.dependsOn = []OperationID{"changed"}
		caller.objects[0] = "changed"
		caller.statements[0].BoundArgs()[0].([]byte)[0] = 'X'
		fresh := resolved.Operations()[0]
		assertOperationAccessors(t, operation, fresh)
		require.Equal(t, "payload", string(fresh.Statements()[0].Args()[0].([]byte)))
	})

	plan, _, _, _ := immutabilityPlanParts(t)
	for _, row := range []struct {
		name   string
		mutate func(*Operation)
	}{
		{"NewPlan operations input", func(o *Operation) {
			o.dependsOn = []OperationID{"changed"}
			o.statements[0].BoundArgs()[0].([]byte)[0] = 'X'
		}},
		{"Plan.Operations return", func(o *Operation) {
			o.dependsOn = []OperationID{"changed"}
			o.objects[0] = "changed"
			o.statements[0].BoundArgs()[0].([]byte)[0] = 'X'
		}},
		{"Plan.TopologicalOperations return", func(o *Operation) {
			o.dependsOn = []OperationID{"changed"}
			o.objects[0] = "changed"
			o.statements[0].BoundArgs()[0].([]byte)[0] = 'X'
		}},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			currentPlan, currentBaseline, currentHistory, currentOperation := immutabilityPlanParts(t)
			if row.name == "NewPlan operations input" {
				operations := []Operation{currentOperation}
				built, buildErr := NewPlan(currentPlan.Profile(), currentBaseline, currentHistory, currentPlan.Decisions(), operations)
				require.NoError(t, buildErr)
				beforeBytes := mustEncodePlan(t, built)
				caller := operations[0]
				operations[0] = Operation{}
				row.mutate(&caller)
				require.Equal(t, currentOperation.ID(), built.Operations()[0].ID())
				if row.name == "NewPlan operations input" {
					// This strict check exposes NewPlan retaining caller operation storage.
					require.Equal(t, beforeBytes, mustEncodePlan(t, built))
				}
				assertOperationAccessors(t, currentOperation, built.Operations()[0])
				require.Equal(t, "payload", string(built.Operations()[0].Statements()[0].Args()[0].([]byte)))
				return
			}
			var got []Operation
			if row.name == "Plan.Operations return" {
				got = plan.Operations()
			} else {
				got, err = plan.TopologicalOperations()
				require.NoError(t, err)
			}
			caller := got[0]
			got[0] = Operation{}
			row.mutate(&caller)
			assertOperationAccessors(t, currentOperation, currentPlan.Operations()[0])
			require.Equal(t, currentOperation.ID(), currentPlan.Operations()[0].ID())
			require.Equal(t, "payload", string(currentPlan.Operations()[0].Statements()[0].Args()[0].([]byte)))
		})
	}
	t.Run("Plan stable order return", func(t *testing.T) {
		order, orderErr := plan.StableOperationOrder()
		require.NoError(t, orderErr)
		order[0] = "changed"
		require.Len(t, append(order, "other"), 2)
		fresh, freshErr := plan.StableOperationOrder()
		require.NoError(t, freshErr)
		require.Equal(t, []OperationID{"operation"}, fresh)
	})
	t.Run("Plan topological order return", func(t *testing.T) {
		order, orderErr := plan.TopologicalOrder()
		require.NoError(t, orderErr)
		order[0] = "changed"
		require.Len(t, append(order, "other"), 2)
		fresh, freshErr := plan.TopologicalOrder()
		require.NoError(t, freshErr)
		require.Equal(t, []OperationID{"operation"}, fresh)
	})
}

func immutabilityRenameFixture(t *testing.T) (ResolvedChanges, []BaselineRename) {
	t.Helper()
	profile := sourceRepairProfile(t)
	definition := schema.TableDef{Schema: "main", Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}}
	object, err := NewCatalogObject("starting", definition)
	require.NoError(t, err)
	baseline, err := NewCatalog(profile, "rename-source", []CatalogObject{object})
	require.NoError(t, err)
	definition.Name = "accounts"
	renameObject, err := NewCatalogObject("starting", definition)
	require.NoError(t, err)
	after, err := NewCatalog(profile, "rename-source", []CatalogObject{renameObject})
	require.NoError(t, err)
	digest := mustCatalogDigest(t, after)
	op, err := NewOperation("rename", OperationRenameTable, nil, []ObjectID{"starting"}, nil, nil, digest,
		[]stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE users RENAME TO accounts"))}, TransactionRequired, false, nil)
	require.NoError(t, err)
	decision, err := NewDecision("rename-decision", DecisionRenameObject, "starting", "main.users", "main.accounts", true, "")
	require.NoError(t, err)
	binding, err := NewBaselineRename(op.ID(), "starting", "main", "accounts")
	require.NoError(t, err)
	step, err := NewResolvedCatalogStep(op.ID(), after)
	require.NoError(t, err)
	renames := []BaselineRename{binding}
	resolved, err := NewResolvedChanges(baseline, []ResolvedCatalogStep{step}, []Decision{decision}, []Operation{op}, nil, renames)
	require.NoError(t, err)
	return resolved, renames
}

func TestImmutabilityMatrixRenameBoundaries(t *testing.T) {
	resolved, renames := immutabilityRenameFixture(t)
	t.Run("ResolvedChanges.Renames return", func(t *testing.T) {
		returned := resolved.Renames()
		returned[0] = BaselineRename{}
		require.Len(t, append(returned, renames[0]), 2)
		require.Equal(t, renames, resolved.Renames())
	})
	t.Run("NewResolvedChanges renames input", func(t *testing.T) {
		renamedInput := []BaselineRename{renames[0]}
		fresh, err := NewResolvedChanges(resolved.BaselineCatalog(), resolved.CatalogSteps(), resolved.Decisions(), resolved.Operations(), nil, renamedInput)
		require.NoError(t, err)
		want := fresh.Renames()
		renamedInput[0] = BaselineRename{}
		require.Len(t, append(renamedInput, renames[0]), 2)
		require.Equal(t, want, fresh.Renames())
	})
}

func TestImmutabilityMatrixEncodedBytes(t *testing.T) {
	plan, _, _, _ := immutabilityPlanParts(t)
	canonical := mustEncodePlan(t, plan)
	t.Run("Decode bytes input", func(t *testing.T) {
		input := append([]byte(nil), canonical...)
		decoded, err := Decode(input)
		require.NoError(t, err)
		for i := range input {
			input[i] = 'x'
		}
		require.Equal(t, canonical, mustEncodePlan(t, decoded))
		require.Equal(t, plan.ID(), decoded.ID())
		require.Equal(t, plan.Operations(), decoded.Operations())
	})
	t.Run("Encode output", func(t *testing.T) {
		output := mustEncodePlan(t, plan)
		for i := range output {
			output[i] = 'x'
		}
		require.Equal(t, canonical, mustEncodePlan(t, plan))
	})
	t.Run("Decode and re-encode separation", func(t *testing.T) {
		decoded, err := Decode(canonical)
		require.NoError(t, err)
		first := mustEncodePlan(t, decoded)
		for i := range first {
			first[i] = 'x'
		}
		second := mustEncodePlan(t, decoded)
		for i := range second {
			second[i] = 'y'
		}
		require.Equal(t, canonical, mustEncodePlan(t, decoded))
	})
}

func TestImmutabilityMatrixExactBoundaryRows(t *testing.T) {
	for _, row := range []struct {
		name   string
		mutate func(Operation, []stmt.Statement, []any)
	}{
		{"NewOperation forward statement replacement", func(op Operation, statements []stmt.Statement, _ []any) {
			statements[0] = stmt.New(sqltext.Text("SELECT changed"))
			require.Equal(t, "SELECT ?", op.Statements()[0].SQL())
		}},
		{"NewOperation forward statement arguments", func(op Operation, statements []stmt.Statement, _ []any) {
			statements[0].BoundArgs()[0].([]byte)[0] = 'X'
			require.Equal(t, "payload", string(op.Statements()[0].Args()[0].([]byte)))
		}},
		{"NewOperation forward statement bytes", func(op Operation, _ []stmt.Statement, args []any) {
			args[0].([]byte)[0] = 'X'
			require.Equal(t, "payload", string(op.Statements()[0].Args()[0].([]byte)))
		}},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			payload := []byte("payload")
			args := []any{payload}
			statements := []stmt.Statement{stmt.New(sqltext.Text("SELECT ?"), args...)}
			op, err := NewOperation("operation", OperationNativeSQL, nil, []ObjectID{"starting"}, nil, nil, Digest{1}, statements, TransactionRequired, false, nil)
			require.NoError(t, err)
			row.mutate(op, statements, args)
		})
	}
	for _, row := range []struct {
		name   string
		mutate func(Operation, []stmt.Statement, []any)
	}{
		{"NewOperation reverse statement replacement", func(op Operation, statements []stmt.Statement, _ []any) {
			statements[0] = stmt.New(sqltext.Text("SELECT changed"))
			require.Equal(t, "SELECT ?", op.ReverseStatements()[0].SQL())
		}},
		{"NewOperation reverse statement arguments", func(op Operation, statements []stmt.Statement, _ []any) {
			statements[0].BoundArgs()[0].([]byte)[0] = 'X'
			require.Equal(t, "reverse", string(op.ReverseStatements()[0].Args()[0].([]byte)))
		}},
		{"NewOperation reverse statement bytes", func(op Operation, _ []stmt.Statement, args []any) {
			args[0].([]byte)[0] = 'X'
			require.Equal(t, "reverse", string(op.ReverseStatements()[0].Args()[0].([]byte)))
		}},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			payload := []byte("reverse")
			args := []any{payload}
			statements := []stmt.Statement{stmt.New(sqltext.Text("SELECT ?"), args...)}
			op, err := NewOperation("operation", OperationNativeSQL, nil, []ObjectID{"starting"}, nil, nil, Digest{1}, []stmt.Statement{stmt.New(sqltext.Text("SELECT ?"), []byte("forward"))}, TransactionRequired, true, statements)
			require.NoError(t, err)
			row.mutate(op, statements, args)
		})
	}

	for _, row := range []struct {
		name   string
		mutate func([]stmt.Statement, []any)
	}{
		{"NewOperationStatements input statement replacement", func(statements []stmt.Statement, _ []any) { statements[0] = stmt.New(sqltext.Text("SELECT changed")) }},
		{"NewOperationStatements input arguments", func(statements []stmt.Statement, _ []any) { statements[0].BoundArgs()[0].([]byte)[0] = 'X' }},
		{"NewOperationStatements input bytes", func(_ []stmt.Statement, args []any) { args[0].([]byte)[0] = 'X' }},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			payload := []byte("payload")
			args := []any{payload}
			statements := []stmt.Statement{stmt.New(sqltext.Text("SELECT ?"), args...)}
			copied, err := NewOperationStatements(statements)
			require.NoError(t, err)
			row.mutate(statements, args)
			require.Equal(t, "SELECT ?", copied[0].SQL())
			require.Equal(t, "payload", string(copied[0].Args()[0].([]byte)))
		})
	}
}

func TestImmutabilityMatrixCatalogExactRows(t *testing.T) {
	for _, row := range []struct {
		name   string
		mutate func(*Catalog)
	}{
		{"NewResolvedCatalogStep source identity", func(c *Catalog) { c.sourceIdentity = "changed" }},
		{"NewResolvedCatalogStep engine identity", func(c *Catalog) { c.physical.Engine.Dialect = "changed" }},
		{"NewResolvedCatalogStep object ID", func(c *Catalog) { c.physical.Objects[0].ID = "changed" }},
		{"NewResolvedCatalogStep nested column", func(c *Catalog) { c.physical.Objects[0].Columns[0].Name = "changed" }},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			_, baseline, _, operation, _ := immutabilityResolvedFixture(t)
			step, err := NewResolvedCatalogStep(operation.ID(), baseline)
			require.NoError(t, err)
			want := mustCatalogDigest(t, step.Catalog())
			row.mutate(&baseline)
			require.Equal(t, want, mustCatalogDigest(t, step.Catalog()))
			assertCatalogIdentityAndLookup(t, step.Catalog(), "resolved-source", "starting")
		})
	}
	for _, row := range []struct {
		name   string
		mutate func(*Catalog)
	}{
		{"ResolvedCatalogStep.Catalog source identity", func(c *Catalog) { c.sourceIdentity = "changed" }},
		{"ResolvedCatalogStep.Catalog engine identity", func(c *Catalog) { c.physical.Engine.Dialect = "changed" }},
		{"ResolvedCatalogStep.Catalog object ID", func(c *Catalog) { c.physical.Objects[0].ID = "changed" }},
		{"ResolvedCatalogStep.Catalog nested column", func(c *Catalog) { c.physical.Objects[0].Columns[0].Name = "changed" }},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			_, baseline, _, operation, _ := immutabilityResolvedFixture(t)
			step, err := NewResolvedCatalogStep(operation.ID(), baseline)
			require.NoError(t, err)
			returned := step.Catalog()
			want := mustCatalogDigest(t, step.Catalog())
			row.mutate(&returned)
			require.Equal(t, want, mustCatalogDigest(t, step.Catalog()))
			assertCatalogIdentityAndLookup(t, step.Catalog(), "resolved-source", "starting")
		})
	}
	for _, row := range []struct {
		name   string
		mutate func(*Catalog)
	}{
		{"ResolvedChanges.BaselineCatalog source", func(c *Catalog) { c.sourceIdentity = "changed" }},
		{"ResolvedChanges.BaselineCatalog engine", func(c *Catalog) { c.physical.Engine.Dialect = "changed" }},
		{"ResolvedChanges.BaselineCatalog object", func(c *Catalog) { c.physical.Objects[0].ID = "changed" }},
		{"ResolvedChanges.BaselineCatalog column", func(c *Catalog) { c.physical.Objects[0].Columns[0].Name = "changed" }},
		{"ResolvedChanges.TargetCatalog source", func(c *Catalog) { c.sourceIdentity = "changed" }},
		{"ResolvedChanges.TargetCatalog engine", func(c *Catalog) { c.physical.Engine.Dialect = "changed" }},
		{"ResolvedChanges.TargetCatalog object", func(c *Catalog) { c.physical.Objects[0].ID = "changed" }},
		{"ResolvedChanges.TargetCatalog column", func(c *Catalog) { c.physical.Objects[0].Columns[0].Name = "changed" }},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			resolved, _, _, _, _ := immutabilityResolvedFixture(t)
			var returned Catalog
			if len(row.name) > len("ResolvedChanges.BaselineCatalog") && row.name[:len("ResolvedChanges.BaselineCatalog")] == "ResolvedChanges.BaselineCatalog" {
				returned = resolved.BaselineCatalog()
			} else {
				returned = resolved.TargetCatalog()
			}
			before := mustCatalogDigest(t, returned)
			row.mutate(&returned)
			if len(row.name) > len("ResolvedChanges.BaselineCatalog") && row.name[:len("ResolvedChanges.BaselineCatalog")] == "ResolvedChanges.BaselineCatalog" {
				require.Equal(t, before, mustCatalogDigest(t, resolved.BaselineCatalog()))
				assertCatalogIdentityAndLookup(t, resolved.BaselineCatalog(), "resolved-source", "starting")
			} else {
				require.Equal(t, before, mustCatalogDigest(t, resolved.TargetCatalog()))
				assertCatalogIdentityAndLookup(t, resolved.TargetCatalog(), "resolved-source", "starting")
			}
		})
	}
}
