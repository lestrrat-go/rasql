package changeplan_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func tableDefinition(name string, withTitle bool) schema.TableDef {
	columns := []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}
	if withTitle {
		columns = append(columns, schema.ColumnDef{Name: "title", Type: schema.TextType{}})
	}
	return schema.TableDef{Schema: "main", Name: name, Columns: columns, PrimaryKey: []string{"id"}}
}

func catalogForObjects(t *testing.T, profile changeplan.Profile, source string, objects ...changeplan.CatalogObject) changeplan.Catalog {
	t.Helper()
	catalog, err := changeplan.NewCatalog(profile, source, objects)
	require.NoError(t, err)
	return catalog
}

func operationFor(t *testing.T, id changeplan.OperationID, kind changeplan.OperationKind, object changeplan.ObjectID, depends []changeplan.OperationID, pre, post []changeplan.Fact, sql string) changeplan.Operation {
	t.Helper()
	operation, err := changeplan.NewOperation(id, kind, depends, []changeplan.ObjectID{object}, pre, post,
		[]stmt.Statement{stmt.New(sqltext.Text(sql))}, changeplan.TransactionEngineDefault, false, []stmt.Statement{})
	require.NoError(t, err)
	return operation
}

func TestAdjacentResolvedCatalogContract(t *testing.T) {
	profile := testProfile(t)
	const source = "adjacent-source"
	starting, err := changeplan.NewCatalogObject("ordinary", tableDefinition("users", false))
	require.NoError(t, err)
	created, err := changeplan.NewIntroducedBaselineObject(source, "create", tableDefinition("created", false))
	require.NoError(t, err)
	createdObject, err := changeplan.NewCatalogObject(created.ID(), tableDefinition("created", false))
	require.NoError(t, err)
	baseline := catalogForObjects(t, profile, source, starting)
	afterCreate := catalogForObjects(t, profile, source, starting, createdObject)
	renameDefinition, err := changeplan.NewCatalogObject(created.ID(), tableDefinition("renamed", false))
	require.NoError(t, err)
	afterRename := catalogForObjects(t, profile, source, starting, renameDefinition)
	finalDefinition, err := changeplan.NewCatalogObject(created.ID(), tableDefinition("final", false))
	require.NoError(t, err)
	afterSecondRename := catalogForObjects(t, profile, source, starting, finalDefinition)
	alterDefinition, err := changeplan.NewCatalogObject(created.ID(), tableDefinition("final", true))
	require.NoError(t, err)
	afterAlter := catalogForObjects(t, profile, source, starting, alterDefinition)

	createFact, err := changeplan.NewFact(created.ID(), "$", changeplan.FactOperatorPresent, "")
	require.NoError(t, err)
	createdName, err := changeplan.NewFact(created.ID(), "/name", changeplan.FactOperatorEqual, `"created"`)
	require.NoError(t, err)
	renamedName, err := changeplan.NewFact(created.ID(), "/name", changeplan.FactOperatorEqual, `"renamed"`)
	require.NoError(t, err)
	finalName, err := changeplan.NewFact(created.ID(), "/name", changeplan.FactOperatorEqual, `"final"`)
	require.NoError(t, err)
	titleFact, err := changeplan.NewFact(created.ID(), "/columns/1/name", changeplan.FactOperatorEqual, `"title"`)
	require.NoError(t, err)
	droppedFact, err := changeplan.NewFact(created.ID(), "$", changeplan.FactOperatorAbsent, "")
	require.NoError(t, err)
	create := operationFor(t, "create", changeplan.OperationCreateTable, created.ID(), nil, nil, []changeplan.Fact{createFact}, "CREATE TABLE created (id INTEGER)")
	rename := operationFor(t, "rename", changeplan.OperationRenameTable, created.ID(), []changeplan.OperationID{"create"}, []changeplan.Fact{createdName}, []changeplan.Fact{renamedName}, "ALTER TABLE created RENAME TO renamed")
	renameTwo := operationFor(t, "rename-two", changeplan.OperationRenameTable, created.ID(), []changeplan.OperationID{"rename"}, []changeplan.Fact{renamedName}, []changeplan.Fact{finalName}, "ALTER TABLE renamed RENAME TO final")
	alter := operationFor(t, "alter", changeplan.OperationAddColumn, created.ID(), []changeplan.OperationID{"rename-two"}, []changeplan.Fact{finalName}, []changeplan.Fact{titleFact}, "ALTER TABLE final ADD COLUMN title TEXT")
	drop := operationFor(t, "drop", changeplan.OperationDropTable, created.ID(), []changeplan.OperationID{"alter"}, []changeplan.Fact{titleFact}, []changeplan.Fact{droppedFact}, "DROP TABLE final")
	renameBinding, err := changeplan.NewBaselineRename("rename", created.ID(), "main", "renamed")
	require.NoError(t, err)
	renameBindingTwo, err := changeplan.NewBaselineRename("rename-two", created.ID(), "main", "final")
	require.NoError(t, err)
	decisions := []changeplan.Decision{
		mustDecision(t, "rename-decision", changeplan.DecisionRenameObject, created.ID(), "main.created", "main.renamed", ""),
		mustDecision(t, "rename-two-decision", changeplan.DecisionRenameObject, created.ID(), "main.renamed", "main.final", ""),
		mustDecision(t, "drop-decision", changeplan.DecisionAcceptDestructive, created.ID(), "", "", "approved"),
	}
	steps := []changeplan.ResolvedCatalogStep{}
	for _, pair := range []struct {
		operation changeplan.Operation
		catalog   changeplan.Catalog
	}{{create, afterCreate}, {rename, afterRename}, {renameTwo, afterSecondRename}, {alter, afterAlter}, {drop, baseline}} {
		step, stepErr := changeplan.NewResolvedCatalogStep(pair.operation.ID(), pair.catalog)
		require.NoError(t, stepErr)
		steps = append(steps, step)
	}
	resolved, err := changeplan.NewResolvedChanges(baseline, steps, decisions, []changeplan.Operation{create, rename, renameTwo, alter, drop}, []changeplan.BaselineObject{created}, []changeplan.BaselineRename{renameBinding, renameBindingTwo})
	require.NoError(t, err)
	wantStartingID, wantStartingOK := baseline.ObjectID(schema.ObjectTable, "main", "users")
	gotStartingID, gotStartingOK := resolved.TargetCatalog().ObjectID(schema.ObjectTable, "main", "users")
	require.True(t, wantStartingOK)
	require.True(t, gotStartingOK)
	require.Equal(t, wantStartingID, gotStartingID)
	require.Len(t, resolved.CatalogSteps(), 5)

	badSteps := append([]changeplan.ResolvedCatalogStep(nil), steps...)
	badSteps[1], badSteps[2] = badSteps[2], badSteps[1]
	_, err = changeplan.NewResolvedChanges(baseline, badSteps, decisions, []changeplan.Operation{create, rename, renameTwo, alter, drop}, []changeplan.BaselineObject{created}, []changeplan.BaselineRename{renameBinding, renameBindingTwo})
	require.Error(t, err)
	_, err = changeplan.NewResolvedChanges(baseline, steps[:4], decisions, []changeplan.Operation{create, rename, renameTwo, alter, drop}, []changeplan.BaselineObject{created}, []changeplan.BaselineRename{renameBinding, renameBindingTwo})
	require.Error(t, err)
	require.Error(t, changeplan.EvaluateFact(baseline, createdName))
	require.Error(t, changeplan.EvaluateFact(afterSecondRename, renamedName))
}

func TestOccurrenceIdentityContract(t *testing.T) {
	profile := testProfile(t)
	ordinaryID := changeplan.ObjectID("e3806f3aad59387841fa56cf1ca8e420ee606c129e61c92aedf4fb145558941e")
	definitions := map[string]schema.TableDef{
		"a": tableDefinition("a", false),
		"b": tableDefinition("b", false),
	}
	publicA := definitions["a"]
	publicA.Schema = "public"
	objects := map[string]changeplan.ObjectID{
		"op-create-a-1": "6ceee67869f9f7832b830bd9efa4038220522c5a5eeb914e2fc966df36f8368b",
		"op-create-a-2": "d87e025df00008ef5ff2b04a6322e8c5fd673ecccb7a46ed6df48b064b0b4c5f",
		"op-create-a-3": "a286e83ef6f1e6d2ccde9d882d9059023290840de6cc090beedc3e59ef58e7dd",
	}
	for operationID, wantID := range objects {
		object, err := changeplan.NewIntroducedBaselineObject("fixture-source", changeplan.OperationID(operationID), publicA)
		require.NoError(t, err)
		require.Equal(t, wantID, object.ID())
		require.Len(t, string(object.ID()), 64)
	}
	ordinary, err := changeplan.NewBaselineObject(ordinaryID, "table", "public", "a")
	require.NoError(t, err)
	require.Empty(t, ordinary.IntroducedBy())

	createFuture, err := changeplan.NewIntroducedBaselineObject("fixture-source", "op-create-a-1", publicA)
	require.NoError(t, err)
	ordinaryObject, err := changeplan.NewCatalogObject(ordinary.ID(), publicA)
	require.NoError(t, err)
	createdObject, err := changeplan.NewCatalogObject(createFuture.ID(), publicA)
	require.NoError(t, err)
	baseline := catalogForObjects(t, profile, "fixture-source", ordinaryObject)
	renameDefinition := definitions["b"]
	renameDefinition.Schema = "public"
	renameObject, err := changeplan.NewCatalogObject(ordinary.ID(), renameDefinition)
	require.NoError(t, err)
	withRename := catalogForObjects(t, profile, "fixture-source", renameObject)
	withCreate := catalogForObjects(t, profile, "fixture-source", renameObject, createdObject)
	gotID, ok := withRename.ObjectID(schema.ObjectTable, "public", "b")
	require.True(t, ok)
	require.Equal(t, ordinaryID, gotID)
	_, ok = withRename.ObjectID(schema.ObjectTable, "public", "a")
	require.False(t, ok)
	gotID, ok = withCreate.ObjectID(schema.ObjectTable, "public", "a")
	require.True(t, ok)
	require.Equal(t, createFuture.ID(), gotID)
	rename, err := operationForOccurrence(t, "rename-a-b", changeplan.OperationRenameTable, ordinary.ID(), nil, "ALTER TABLE a RENAME TO b")
	require.NoError(t, err)
	create, err := operationForOccurrence(t, "op-create-a-1", changeplan.OperationCreateTable, createFuture.ID(), []changeplan.OperationID{"rename-a-b"}, "CREATE TABLE a (id INTEGER)")
	require.NoError(t, err)
	renameBinding, err := changeplan.NewBaselineRename(rename.ID(), ordinary.ID(), "public", "b")
	require.NoError(t, err)
	decisions := []changeplan.Decision{mustDecision(t, "rename-a-b-decision", changeplan.DecisionRenameObject, ordinary.ID(), "public.a", "public.b", "")}
	step1, err := changeplan.NewResolvedCatalogStep(rename.ID(), withRename)
	require.NoError(t, err)
	step2, err := changeplan.NewResolvedCatalogStep(create.ID(), withCreate)
	require.NoError(t, err)
	_, err = changeplan.NewResolvedChanges(baseline, []changeplan.ResolvedCatalogStep{step1, step2}, decisions,
		[]changeplan.Operation{rename, create}, []changeplan.BaselineObject{createFuture}, []changeplan.BaselineRename{renameBinding})
	require.NoError(t, err)
}

func TestOccurrenceReuseSequences(t *testing.T) {
	profile := testProfile(t)
	var gotID changeplan.ObjectID
	var ok bool
	definition := tableDefinition("a", false)
	definition.Schema = "public"
	ordinaryID := changeplan.ObjectID("e3806f3aad59387841fa56cf1ca8e420ee606c129e61c92aedf4fb145558941e")
	ordinaryObject, err := changeplan.NewCatalogObject(ordinaryID, definition)
	require.NoError(t, err)
	ordinaryBaseline := catalogForObjects(t, profile, "fixture-source", ordinaryObject)

	futureTwo, err := changeplan.NewIntroducedBaselineObject("fixture-source", "op-create-a-2", definition)
	require.NoError(t, err)
	futureTwoObject, err := changeplan.NewCatalogObject(futureTwo.ID(), definition)
	require.NoError(t, err)
	dropOrdinary, err := operationForOccurrence(t, "drop-ordinary", changeplan.OperationDropTable, ordinaryObject.ID(), nil, "DROP TABLE a")
	require.NoError(t, err)
	createTwo, err := operationForOccurrence(t, "op-create-a-2", changeplan.OperationCreateTable, futureTwo.ID(), []changeplan.OperationID{"drop-ordinary"}, "CREATE TABLE a (id INTEGER)")
	require.NoError(t, err)
	dropDecision := mustDecision(t, "drop-ordinary-decision", changeplan.DecisionAcceptDestructive, ordinaryObject.ID(), "", "", "approved")
	emptyReuse := make([]changeplan.CatalogObject, 0)
	dropStep, err := changeplan.NewResolvedCatalogStep(dropOrdinary.ID(), catalogForObjects(t, profile, "fixture-source", emptyReuse...))
	require.NoError(t, err)
	createStep, err := changeplan.NewResolvedCatalogStep(createTwo.ID(), catalogForObjects(t, profile, "fixture-source", futureTwoObject))
	require.NoError(t, err)
	_, ok = catalogForObjects(t, profile, "fixture-source", emptyReuse...).ObjectID(schema.ObjectTable, "public", "a")
	require.False(t, ok)
	gotID, ok = createStep.Catalog().ObjectID(schema.ObjectTable, "public", "a")
	require.True(t, ok)
	require.Equal(t, futureTwo.ID(), gotID)
	_, err = changeplan.NewResolvedChanges(ordinaryBaseline, []changeplan.ResolvedCatalogStep{dropStep, createStep}, []changeplan.Decision{dropDecision}, []changeplan.Operation{dropOrdinary, createTwo}, []changeplan.BaselineObject{futureTwo}, []changeplan.BaselineRename{})
	require.NoError(t, err)

	futureOne, err := changeplan.NewIntroducedBaselineObject("fixture-source", "op-create-a-1", definition)
	require.NoError(t, err)
	futureThree, err := changeplan.NewIntroducedBaselineObject("fixture-source", "op-create-a-3", definition)
	require.NoError(t, err)
	futureOneObject, err := changeplan.NewCatalogObject(futureOne.ID(), definition)
	require.NoError(t, err)
	futureThreeObject, err := changeplan.NewCatalogObject(futureThree.ID(), definition)
	require.NoError(t, err)
	empty := make([]changeplan.CatalogObject, 0)
	emptyBaseline := catalogForObjects(t, profile, "fixture-source", empty...)
	createOne, err := operationForOccurrence(t, "op-create-a-1", changeplan.OperationCreateTable, futureOne.ID(), nil, "CREATE TABLE a (id INTEGER)")
	require.NoError(t, err)
	dropOne, err := operationForOccurrence(t, "drop-a-1", changeplan.OperationDropTable, futureOne.ID(), []changeplan.OperationID{"op-create-a-1"}, "DROP TABLE a")
	require.NoError(t, err)
	createThree, err := operationForOccurrence(t, "op-create-a-3", changeplan.OperationCreateTable, futureThree.ID(), []changeplan.OperationID{"drop-a-1"}, "CREATE TABLE a (id INTEGER)")
	require.NoError(t, err)
	stepOne, err := changeplan.NewResolvedCatalogStep(createOne.ID(), catalogForObjects(t, profile, "fixture-source", futureOneObject))
	require.NoError(t, err)
	stepDrop, err := changeplan.NewResolvedCatalogStep(dropOne.ID(), emptyBaseline)
	require.NoError(t, err)
	stepThree, err := changeplan.NewResolvedCatalogStep(createThree.ID(), catalogForObjects(t, profile, "fixture-source", futureThreeObject))
	require.NoError(t, err)
	gotID, ok = stepOne.Catalog().ObjectID(schema.ObjectTable, "public", "a")
	require.True(t, ok)
	require.Equal(t, futureOne.ID(), gotID)
	_, ok = stepDrop.Catalog().ObjectID(schema.ObjectTable, "public", "a")
	require.False(t, ok)
	gotID, ok = stepThree.Catalog().ObjectID(schema.ObjectTable, "public", "a")
	require.True(t, ok)
	require.Equal(t, futureThree.ID(), gotID)
	dropDecisionOne := mustDecision(t, "drop-a-1-decision", changeplan.DecisionAcceptDestructive, futureOne.ID(), "", "", "approved")
	_, err = changeplan.NewResolvedChanges(emptyBaseline, []changeplan.ResolvedCatalogStep{stepOne, stepDrop, stepThree}, []changeplan.Decision{dropDecisionOne}, []changeplan.Operation{createOne, dropOne, createThree}, []changeplan.BaselineObject{futureOne, futureThree}, []changeplan.BaselineRename{})
	require.NoError(t, err)
	require.Equal(t, changeplan.ObjectID("6ceee67869f9f7832b830bd9efa4038220522c5a5eeb914e2fc966df36f8368b"), futureOne.ID())
	require.Equal(t, changeplan.ObjectID("d87e025df00008ef5ff2b04a6322e8c5fd673ecccb7a46ed6df48b064b0b4c5f"), futureTwo.ID())
	require.Equal(t, changeplan.ObjectID("a286e83ef6f1e6d2ccde9d882d9059023290840de6cc090beedc3e59ef58e7dd"), futureThree.ID())
}

func TestResolvedPrefixNegativeContract(t *testing.T) {
	profile := testProfile(t)
	object, err := changeplan.NewCatalogObject("ordinary", tableDefinition("users", false))
	require.NoError(t, err)
	baseline := catalogForObjects(t, profile, "negative-source", object)
	operation, err := operationForOccurrence(t, "native", changeplan.OperationNativeSQL, object.ID(), nil, "SELECT 1")
	require.NoError(t, err)
	decision := mustDecision(t, "native-decision", changeplan.DecisionAcceptNativeSQL, object.ID(), "", "", "approved")
	wrongStep, err := changeplan.NewResolvedCatalogStep("different-operation", baseline)
	require.NoError(t, err)
	_, err = changeplan.NewResolvedChanges(baseline, []changeplan.ResolvedCatalogStep{wrongStep}, []changeplan.Decision{decision}, []changeplan.Operation{operation}, nil, nil)
	require.ErrorContains(t, err, "stable order")
	_, err = changeplan.NewResolvedChanges(baseline, nil, []changeplan.Decision{decision}, []changeplan.Operation{operation}, []changeplan.BaselineObject{}, []changeplan.BaselineRename{})
	require.ErrorContains(t, err, "must be nonnil")
	extraStep, err := changeplan.NewResolvedCatalogStep("extra", baseline)
	require.NoError(t, err)
	_, err = changeplan.NewResolvedChanges(baseline, []changeplan.ResolvedCatalogStep{wrongStep, extraStep}, []changeplan.Decision{decision}, []changeplan.Operation{operation}, nil, nil)
	require.ErrorContains(t, err, "step count")
	duplicateOperation, err := operationForOccurrence(t, "duplicate", changeplan.OperationNativeSQL, object.ID(), []changeplan.OperationID{"native"}, "SELECT 2")
	require.NoError(t, err)
	duplicateStep, err := changeplan.NewResolvedCatalogStep(duplicateOperation.ID(), baseline)
	require.NoError(t, err)
	duplicateDecision := mustDecision(t, "duplicate-decision", changeplan.DecisionAcceptNativeSQL, object.ID(), "", "", "approved")
	_, err = changeplan.NewResolvedChanges(baseline, []changeplan.ResolvedCatalogStep{wrongStep, duplicateStep}, []changeplan.Decision{decision, duplicateDecision}, []changeplan.Operation{operation, duplicateOperation}, nil, nil)
	require.ErrorContains(t, err, "stable order")
	otherProfile := testProfile(t)
	otherObject, err := changeplan.NewCatalogObject("ordinary", tableDefinition("users", false))
	require.NoError(t, err)
	otherBaseline := catalogForObjects(t, otherProfile, "other-source", otherObject)
	otherStep, err := changeplan.NewResolvedCatalogStep(operation.ID(), otherBaseline)
	require.NoError(t, err)
	_, err = changeplan.NewResolvedChanges(baseline, []changeplan.ResolvedCatalogStep{otherStep}, []changeplan.Decision{decision}, []changeplan.Operation{operation}, []changeplan.BaselineObject{}, []changeplan.BaselineRename{})
	require.ErrorContains(t, err, "identity differs")
}

func operationForOccurrence(t *testing.T, id changeplan.OperationID, kind changeplan.OperationKind, object changeplan.ObjectID, depends []changeplan.OperationID, sql string) (changeplan.Operation, error) {
	t.Helper()
	return changeplan.NewOperation(id, kind, depends, []changeplan.ObjectID{object}, nil, nil,
		[]stmt.Statement{stmt.New(sqltext.Text(sql))}, changeplan.TransactionEngineDefault, false, []stmt.Statement{})
}
