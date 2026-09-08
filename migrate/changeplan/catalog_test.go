package changeplan_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestCatalogCopiesDefinitionsAndSupportsResolvedSteps(t *testing.T) {
	profile := testProfile(t)
	definition := schema.TableDef{Schema: "main", Name: "tasks", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}}
	object, err := changeplan.NewCatalogObject("task-id", definition)
	require.NoError(t, err)
	catalog, err := changeplan.NewCatalog(profile, "source", []changeplan.CatalogObject{object})
	require.NoError(t, err)
	definition.Columns[0].Name = "changed"
	require.Equal(t, "id", object.Definition().Columns[0].Name)
	id, ok := catalog.ObjectID(schema.ObjectTable, "main", "tasks")
	require.True(t, ok)
	require.Equal(t, changeplan.ObjectID("task-id"), id)

	afterDefinition := schema.TableDef{Schema: "main", Name: "tasks", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "title", Type: schema.TextType{}},
	}}
	afterObject, err := changeplan.NewCatalogObject(id, afterDefinition)
	require.NoError(t, err)
	after, err := changeplan.NewCatalogLike(catalog, []changeplan.CatalogObject{afterObject})
	require.NoError(t, err)
	fact, err := changeplan.NewFact(id, "/columns/1/name", changeplan.FactOperatorEqual, `"title"`)
	require.NoError(t, err)
	operation, err := changeplan.NewOperation("alter", changeplan.OperationAddColumn, []changeplan.OperationID{}, []changeplan.ObjectID{id}, nil, []changeplan.Fact{fact}, nil, changeplan.TransactionEngineDefault, false, []stmt.Statement{})
	require.NoError(t, err)
	step, err := changeplan.NewResolvedCatalogStep(operation.ID(), after)
	require.NoError(t, err)
	resolved, err := changeplan.NewResolvedChanges(catalog, []changeplan.ResolvedCatalogStep{step}, []changeplan.Decision{}, []changeplan.Operation{operation}, []changeplan.BaselineObject{}, []changeplan.BaselineRename{})
	require.NoError(t, err)
	require.Equal(t, "source", resolved.TargetCatalog().SourceIdentity())
	require.Len(t, resolved.CatalogSteps(), 1)
	nonLeaf, err := changeplan.NewFact(id, "/columns/0", changeplan.FactOperatorEqual, `{"name":"id","ordinal":0}`)
	require.NoError(t, err)
	require.Error(t, changeplan.EvaluateFact(catalog, nonLeaf))
	_, err = changeplan.NewResolvedChanges(catalog, nil, []changeplan.Decision{}, []changeplan.Operation{}, []changeplan.BaselineObject{}, []changeplan.BaselineRename{})
	require.Error(t, err)
}

func TestIntroducedBaselineObjectGoldens(t *testing.T) {
	definition := schema.TableDef{Schema: "public", Name: "a", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}}
	cases := map[string]string{
		"op-create-a-1": "6ceee67869f9f7832b830bd9efa4038220522c5a5eeb914e2fc966df36f8368b",
		"op-create-a-2": "d87e025df00008ef5ff2b04a6322e8c5fd673ecccb7a46ed6df48b064b0b4c5f",
		"op-create-a-3": "a286e83ef6f1e6d2ccde9d882d9059023290840de6cc090beedc3e59ef58e7dd",
	}
	for operation, expected := range cases {
		object, err := changeplan.NewIntroducedBaselineObject("fixture-source", changeplan.OperationID(operation), definition)
		require.NoError(t, err)
		require.Equal(t, changeplan.ObjectID(expected), object.ID())
		require.Equal(t, changeplan.OperationID(operation), object.IntroducedBy())
	}
	starting, err := changeplan.NewBaselineObject("e3806f3aad59387841fa56cf1ca8e420ee606c129e61c92aedf4fb145558941e", "table", "public", "a")
	require.NoError(t, err)
	require.Empty(t, starting.IntroducedBy())
}
