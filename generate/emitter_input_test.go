package generate_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/stretchr/testify/require"
)

// plainEmitterParts is the smallest catalog and policy an emitter input can be
// built from: one table, one column, no mappings. The parts are returned rather
// than the input itself so a test can alter one of them and see what
// NewEmitterInput derives from the altered set.
func plainEmitterParts(t *testing.T) (compilerir.PhysicalCatalog, compilerir.MappingConfig, compilerir.GoConfig) {
	t.Helper()
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite", Version: "3"}, Objects: []compilerir.PhysicalObject{{ID: "users", Kind: "table", Name: "users", Columns: []compilerir.PhysicalColumn{{Name: "id", Ordinal: 0, LogicalKind: "integer"}}}}}
	config := compilerir.GoConfig{Package: "store", Output: "generated", Emitter: "compact", Objects: []compilerir.ObjectGoName{{ID: "users", File: "users_gen.go"}}}
	return catalog, compilerir.MappingConfig{}, config
}

func plainEmitterFixture(t *testing.T) generate.EmitterInput {
	t.Helper()
	catalog, mappings, config := plainEmitterParts(t)
	in, err := generate.NewEmitterInput(catalog, mappings, config)
	require.NoError(t, err)
	return in
}

// emitterParts adds a nullable column carrying a scalar mapping, so a test can
// check what the derived Go model binds that column to.
func emitterParts(t *testing.T) (compilerir.PhysicalCatalog, compilerir.MappingConfig, compilerir.GoConfig) {
	t.Helper()
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite", Version: "3"}, Objects: []compilerir.PhysicalObject{{
		ID: "users", Kind: "table", Name: "users", Columns: []compilerir.PhysicalColumn{
			{Name: "id", Ordinal: 0, LogicalKind: "integer"},
			{Name: "status", Ordinal: 1, LogicalKind: "text", Nullable: true},
		},
	}}}
	mapping := compilerir.ScalarMapping{Name: "status", Match: compilerir.NativeMatch{LogicalKind: "text"}, GoType: "Status", NullableGoType: "NullableStatus", Codec: "status"}
	mappings := compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{mapping}}
	config := compilerir.GoConfig{Package: "store", Output: "generated", Emitter: "compact", Objects: []compilerir.ObjectGoName{{ID: "users", File: "users_gen.go"}}}
	return catalog, mappings, config
}

func emitterFixture(t *testing.T) generate.EmitterInput {
	t.Helper()
	catalog, mappings, config := emitterParts(t)
	in, err := generate.NewEmitterInput(catalog, mappings, config)
	require.NoError(t, err)
	return in
}

// TestNewEmitterInputDerivesTheCanonicalModels pins the invariant the input's
// unexported models rest on: they are what BuildSemantic and BuildGo produce
// for the same catalog and policy, not something a caller chose.
func TestNewEmitterInputDerivesTheCanonicalModels(t *testing.T) {
	catalog, mappings, config := emitterParts(t)
	in, err := generate.NewEmitterInput(catalog, mappings, config)
	require.NoError(t, err)

	semantic, diagnostics := compilerir.BuildSemantic(catalog, mappings, nil)
	require.Empty(t, diagnostics)
	config.Scalars = mappings.Scalars
	want, diagnostics := compilerir.BuildGo(semantic, config)
	require.Empty(t, diagnostics)
	require.Equal(t, want, in.GoModel())
}

// TestNewEmitterInputFillsGenerationScalars covers a caller that configures its
// scalar mappings once, in the mapping config. The Go builder reads them from
// the generation config, and the constructor is what carries them across.
func TestNewEmitterInputFillsGenerationScalars(t *testing.T) {
	catalog, mappings, config := emitterParts(t)
	require.Empty(t, config.Scalars, "the fixture leaves generation scalars unset")
	in, err := generate.NewEmitterInput(catalog, mappings, config)
	require.NoError(t, err)

	status, ok := goColumn(in.GoModel(), "users", "status")
	require.True(t, ok)
	require.Equal(t, "NullableStatus", status.GoType)
	require.Equal(t, "status", status.Codec)
}

func TestNewEmitterInputOwnsConstructorInputs(t *testing.T) {
	catalog, mappings, config := emitterParts(t)
	in, err := generate.NewEmitterInput(catalog, mappings, config)
	require.NoError(t, err)
	before := in.GoModel()

	catalog.Objects[0].Columns[0].Name = "changed"
	mappings.Scalars[0].GoType = "Changed"
	config.Objects[0].File = "changed.go"

	require.Equal(t, before, in.GoModel())
}

// TestGoModelHandsBackACopy covers the other direction: what an accessor
// returns is the caller's to mutate.
func TestGoModelHandsBackACopy(t *testing.T) {
	in := emitterFixture(t)
	model := in.GoModel()
	model.Objects[0].Columns[0].GoType = "Changed"
	require.NotEqual(t, "Changed", in.GoModel().Objects[0].Columns[0].GoType)
}

func TestNewEmitterInputRejectsUnusableGenerationPolicy(t *testing.T) {
	cases := []struct {
		name, want string
		edit       func(*compilerir.GoConfig)
	}{
		{
			name: "no object policy",
			want: "does not cover every catalog object",
			edit: func(config *compilerir.GoConfig) { config.Objects = nil },
		},
		{
			name: "object absent from catalog",
			want: "is absent from catalog",
			edit: func(config *compilerir.GoConfig) {
				config.Objects[0].ID = "ghosts"
			},
		},
		{
			name: "duplicated object",
			want: "is duplicated",
			edit: func(config *compilerir.GoConfig) {
				// The copy names no output file, so it reaches the duplicate
				// check rather than tripping ValidateGo's duplicate-file check
				// on the way there.
				second := config.Objects[0]
				second.File = ""
				config.Objects = append(config.Objects, second)
			},
		},
		{
			name: "query names no semantic query",
			want: "is absent from semantic model",
			edit: func(config *compilerir.GoConfig) {
				config.Queries = []compilerir.QueryGoName{{ID: "find_users", Function: "FindUsers"}}
			},
		},
		{
			name: "no emitter",
			want: "must be compact or legacy",
			edit: func(config *compilerir.GoConfig) { config.Emitter = "" },
		},
		{
			name: "no output",
			want: "package and output are required",
			edit: func(config *compilerir.GoConfig) { config.Output = "" },
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			catalog, mappings, config := plainEmitterParts(t)
			test.edit(&config)
			_, err := generate.NewEmitterInput(catalog, mappings, config)
			require.ErrorContains(t, err, test.want)
		})
	}
}

// TestNewEmitterInputRejectsAnUnmappedCatalog covers a catalog the mapping
// config does not cover: the semantic builder has no scalar for the column, and
// the constructor reports that rather than emitting a store around it.
func TestNewEmitterInputRejectsAnUnmappedCatalog(t *testing.T) {
	catalog, mappings, config := plainEmitterParts(t)
	catalog.Objects[0].Columns = append(catalog.Objects[0].Columns, compilerir.PhysicalColumn{
		Name: "balance", Ordinal: 1, LogicalKind: "native",
		Native: &compilerir.NativeType{Dialect: "sqlite", Name: "MONEY", Kind: "other"},
	})
	_, err := generate.NewEmitterInput(catalog, mappings, config)
	require.ErrorContains(t, err, "opaque native type requires an explicit mapping")
}

// TestNewEmitterInputDerivesViewWriteShapes covers a view, whose columns are
// all forbidden to write: the derived Go model still carries create and patch
// shapes, and they carry no fields.
func TestNewEmitterInputDerivesViewWriteShapes(t *testing.T) {
	catalog, mappings, config := plainEmitterParts(t)
	catalog.Objects[0].Kind = "view"
	in, err := generate.NewEmitterInput(catalog, mappings, config)
	require.NoError(t, err)

	object := in.GoModel().Objects[0]
	require.NotNil(t, object.Create)
	require.NotNil(t, object.Patch)
	require.Empty(t, object.Create.Fields)
	require.Empty(t, object.Patch.Fields)
}

// TestNewEmitterInputDerivesRelationsFromMappings covers a many-through
// relation that exists only because the mapping config declares it: it is in
// the derived Go model when the mappings are passed and gone when they are not.
func TestNewEmitterInputDerivesRelationsFromMappings(t *testing.T) {
	catalog, mappings, config := manyThroughEmitterParts(t)
	in, err := generate.NewEmitterInput(catalog, mappings, config)
	require.NoError(t, err)

	relation, ok := goRelation(in.GoModel(), "users", "Roles")
	require.True(t, ok, "the mapped many-through relation reached the Go model")
	require.Equal(t, "many_through", relation.Kind)
	require.Equal(t, compilerir.ObjectID("roles"), relation.Target)
	require.NotNil(t, relation.Through)
	require.Equal(t, compilerir.ObjectID("user_roles"), relation.Through.Object)
	require.Equal(t, []string{"user_id"}, relation.Through.SourceFrom)
	require.Equal(t, []string{"role_id"}, relation.Through.TargetFrom)

	without, err := generate.NewEmitterInput(catalog, compilerir.MappingConfig{}, config)
	require.NoError(t, err)
	_, ok = goRelation(without.GoModel(), "users", "Roles")
	require.False(t, ok, "the relation exists only because the mappings declare it")
}

func manyThroughEmitterParts(t *testing.T) (compilerir.PhysicalCatalog, compilerir.MappingConfig, compilerir.GoConfig) {
	t.Helper()
	integer := func(name string, ordinal int) compilerir.PhysicalColumn {
		return compilerir.PhysicalColumn{Name: name, Ordinal: ordinal, LogicalKind: "integer"}
	}
	users := compilerir.PhysicalObject{ID: "users", Kind: "table", Name: "users", Columns: []compilerir.PhysicalColumn{integer("id", 0)}}
	roles := compilerir.PhysicalObject{ID: "roles", Kind: "table", Name: "roles", Columns: []compilerir.PhysicalColumn{integer("id", 0)}}
	userRoles := compilerir.PhysicalObject{ID: "user_roles", Kind: "table", Name: "user_roles", Columns: []compilerir.PhysicalColumn{integer("user_id", 0), integer("role_id", 1)}, Constraints: []compilerir.PhysicalConstraint{
		{Kind: "foreign_key", Name: "user_roles_user", Columns: []string{"user_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}},
		{Kind: "foreign_key", Name: "user_roles_role", Columns: []string{"role_id"}, Reference: &compilerir.ForeignReference{Object: "roles", Columns: []string{"id"}}},
	}}
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite", Version: "3"}, Objects: []compilerir.PhysicalObject{users, roles, userRoles}}
	mappings := compilerir.MappingConfig{Relations: []compilerir.RelationMapping{{Name: "Roles", Source: "users", From: []string{"id"}, Target: "roles", To: []string{"id"}, Through: compilerir.ThroughMapping{Object: "user_roles", SourceFrom: []string{"user_id"}, SourceTo: []string{"id"}, TargetFrom: []string{"role_id"}, TargetTo: []string{"id"}}}}}
	config := compilerir.GoConfig{Package: "store", Output: "generated", Emitter: "compact", Objects: []compilerir.ObjectGoName{{ID: "users"}, {ID: "roles"}, {ID: "user_roles"}}}
	return catalog, mappings, config
}

func goColumn(model compilerir.GoModel, object compilerir.ObjectID, name string) (compilerir.GoColumn, bool) {
	for _, candidate := range model.Objects {
		if candidate.ID != object {
			continue
		}
		for _, column := range candidate.Columns {
			if column.Name == name {
				return column, true
			}
		}
	}
	return compilerir.GoColumn{}, false
}

func goRelation(model compilerir.GoModel, object compilerir.ObjectID, name string) (compilerir.GoRelation, bool) {
	for _, candidate := range model.Objects {
		if candidate.ID != object {
			continue
		}
		for _, relation := range candidate.Relations {
			if relation.Name == name {
				return relation, true
			}
		}
	}
	return compilerir.GoRelation{}, false
}
