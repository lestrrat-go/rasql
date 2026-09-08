package changeplan

import (
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

type resolvedContractProfileSource struct{ value engineprofile.Profile }

func (s resolvedContractProfileSource) ID() string                       { return s.value.ID }
func (s resolvedContractProfileSource) Engine() EngineID                 { return s.value.Engine }
func (s resolvedContractProfileSource) Version() EngineVersion           { return s.value.Version }
func (s resolvedContractProfileSource) Capabilities() EngineCapabilities { return s.value.Capabilities }
func (s resolvedContractProfileSource) Limits() EngineLimits             { return s.value.Limits }

func resolvedContractProfile(t *testing.T) Profile {
	t.Helper()
	value, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 35})
	require.NoError(t, err)
	profile, err := NewProfile(resolvedContractProfileSource{value: value})
	require.NoError(t, err)
	return profile
}

func resolvedContractDecision(t *testing.T, id DecisionID, kind DecisionKind, object ObjectID, from, to, reason string) Decision {
	t.Helper()
	decision, err := NewDecision(id, kind, object, from, to, true, reason)
	require.NoError(t, err)
	return decision
}

func tableDefinition(name string, withTitle bool) schema.TableDef {
	columns := []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}
	if withTitle {
		columns = append(columns, schema.ColumnDef{Name: "title", Type: schema.TextType{}})
	}
	return schema.TableDef{Schema: "main", Name: name, Columns: columns, PrimaryKey: []string{"id"}}
}

func catalogForObjects(t *testing.T, profile Profile, source string, objects ...CatalogObject) Catalog {
	t.Helper()
	catalog, err := NewCatalog(profile, source, objects)
	require.NoError(t, err)
	return catalog
}

func operationFor(t *testing.T, id OperationID, kind OperationKind, object ObjectID, depends []OperationID, pre, post []Fact, after Catalog, sql string) Operation {
	t.Helper()
	digest, err := CatalogDigest(after)
	require.NoError(t, err)
	operation, err := NewOperation(id, kind, depends, []ObjectID{object}, pre, post, digest,
		[]stmt.Statement{stmt.New(sqltext.Text(sql))}, TransactionEngineDefault, false, []stmt.Statement{})
	require.NoError(t, err)
	return operation
}

func TestAdjacentResolvedCatalogContract(t *testing.T) {
	profile := resolvedContractProfile(t)
	const source = "adjacent-source"
	starting, err := NewCatalogObject("ordinary", tableDefinition("users", false))
	require.NoError(t, err)
	created, err := NewIntroducedBaselineObject(source, "create", tableDefinition("created", false))
	require.NoError(t, err)
	createdObject, err := NewCatalogObject(created.ID(), tableDefinition("created", false))
	require.NoError(t, err)
	baseline := catalogForObjects(t, profile, source, starting)
	afterCreate := catalogForObjects(t, profile, source, starting, createdObject)
	renameDefinition, err := NewCatalogObject(created.ID(), tableDefinition("renamed", false))
	require.NoError(t, err)
	afterRename := catalogForObjects(t, profile, source, starting, renameDefinition)
	finalDefinition, err := NewCatalogObject(created.ID(), tableDefinition("final", false))
	require.NoError(t, err)
	afterSecondRename := catalogForObjects(t, profile, source, starting, finalDefinition)
	alterDefinition, err := NewCatalogObject(created.ID(), tableDefinition("final", true))
	require.NoError(t, err)
	afterAlter := catalogForObjects(t, profile, source, starting, alterDefinition)

	createFact, err := NewFact(created.ID(), "$", FactOperatorPresent, "")
	require.NoError(t, err)
	createdName, err := NewFact(created.ID(), "/name", FactOperatorEqual, `"created"`)
	require.NoError(t, err)
	renamedName, err := NewFact(created.ID(), "/name", FactOperatorEqual, `"renamed"`)
	require.NoError(t, err)
	finalName, err := NewFact(created.ID(), "/name", FactOperatorEqual, `"final"`)
	require.NoError(t, err)
	titleFact, err := NewFact(created.ID(), "/columns/1/name", FactOperatorEqual, `"title"`)
	require.NoError(t, err)
	droppedFact, err := NewFact(created.ID(), "$", FactOperatorAbsent, "")
	require.NoError(t, err)
	create := operationFor(t, "create", OperationCreateTable, created.ID(), nil, nil, []Fact{createFact}, afterCreate, "CREATE TABLE created (id INTEGER)")
	rename := operationFor(t, "rename", OperationRenameTable, created.ID(), []OperationID{"create"}, []Fact{createdName}, []Fact{renamedName}, afterRename, "ALTER TABLE created RENAME TO renamed")
	renameTwo := operationFor(t, "rename-two", OperationRenameTable, created.ID(), []OperationID{"rename"}, []Fact{renamedName}, []Fact{finalName}, afterSecondRename, "ALTER TABLE renamed RENAME TO final")
	alter := operationFor(t, "alter", OperationAddColumn, created.ID(), []OperationID{"rename-two"}, []Fact{finalName}, []Fact{titleFact}, afterAlter, "ALTER TABLE final ADD COLUMN title TEXT")
	drop := operationFor(t, "drop", OperationDropTable, created.ID(), []OperationID{"alter"}, []Fact{titleFact}, []Fact{droppedFact}, baseline, "DROP TABLE final")
	renameBinding, err := NewBaselineRename("rename", created.ID(), "main", "renamed")
	require.NoError(t, err)
	renameBindingTwo, err := NewBaselineRename("rename-two", created.ID(), "main", "final")
	require.NoError(t, err)
	decisions := []Decision{
		resolvedContractDecision(t, "rename-decision", DecisionRenameObject, created.ID(), "main.created", "main.renamed", ""),
		resolvedContractDecision(t, "rename-two-decision", DecisionRenameObject, created.ID(), "main.renamed", "main.final", ""),
		resolvedContractDecision(t, "drop-decision", DecisionAcceptDestructive, created.ID(), "", "", "approved"),
	}
	steps := []ResolvedCatalogStep{}
	for _, pair := range []struct {
		operation Operation
		catalog   Catalog
	}{{create, afterCreate}, {rename, afterRename}, {renameTwo, afterSecondRename}, {alter, afterAlter}, {drop, baseline}} {
		step, stepErr := NewResolvedCatalogStep(pair.operation.ID(), pair.catalog)
		require.NoError(t, stepErr)
		steps = append(steps, step)
	}
	resolved, err := NewResolvedChanges(baseline, steps, decisions, []Operation{create, rename, renameTwo, alter, drop}, []BaselineObject{created}, []BaselineRename{renameBinding, renameBindingTwo})
	require.NoError(t, err)
	wantStartingID, wantStartingOK := baseline.ObjectID(schema.ObjectTable, "main", "users")
	gotStartingID, gotStartingOK := resolved.TargetCatalog().ObjectID(schema.ObjectTable, "main", "users")
	require.True(t, wantStartingOK)
	require.True(t, gotStartingOK)
	require.Equal(t, wantStartingID, gotStartingID)
	require.Len(t, resolved.CatalogSteps(), 5)

	badSteps := append([]ResolvedCatalogStep(nil), steps...)
	badSteps[1], badSteps[2] = badSteps[2], badSteps[1]
	_, err = NewResolvedChanges(baseline, badSteps, decisions, []Operation{create, rename, renameTwo, alter, drop}, []BaselineObject{created}, []BaselineRename{renameBinding, renameBindingTwo})
	require.ErrorIs(t, err, ErrInvalidPlan)
	require.ErrorContains(t, err, "not in stable order")
	_, err = NewResolvedChanges(baseline, steps[:4], decisions, []Operation{create, rename, renameTwo, alter, drop}, []BaselineObject{created}, []BaselineRename{renameBinding, renameBindingTwo})
	require.ErrorIs(t, err, ErrInvalidPlan)
	require.ErrorContains(t, err, "step count does not match operations")
	err = EvaluateFact(baseline, createdName)
	require.ErrorIs(t, err, ErrFactMismatch)
	err = EvaluateFact(afterSecondRename, renamedName)
	require.ErrorIs(t, err, ErrFactMismatch)
}

type resolvedContractChain struct {
	baseline   Catalog
	steps      []ResolvedCatalogStep
	operations []Operation
	decisions  []Decision
	future     []BaselineObject
	renamed    []BaselineRename
	catalogs   []Catalog
}

func makeResolvedContractChain(t *testing.T) resolvedContractChain {
	t.Helper()
	profile := resolvedContractProfile(t)
	const source = "adjacent-source"
	starting, err := NewCatalogObject("ordinary", tableDefinition("users", false))
	require.NoError(t, err)
	created, err := NewIntroducedBaselineObject(source, "create", tableDefinition("created", false))
	require.NoError(t, err)
	createdObject, err := NewCatalogObject(created.ID(), tableDefinition("created", false))
	require.NoError(t, err)
	baseline := catalogForObjects(t, profile, source, starting)
	afterCreate := catalogForObjects(t, profile, source, starting, createdObject)
	renameDefinition, err := NewCatalogObject(created.ID(), tableDefinition("renamed", false))
	require.NoError(t, err)
	afterRename := catalogForObjects(t, profile, source, starting, renameDefinition)
	finalDefinition, err := NewCatalogObject(created.ID(), tableDefinition("final", false))
	require.NoError(t, err)
	afterSecondRename := catalogForObjects(t, profile, source, starting, finalDefinition)
	alterDefinition, err := NewCatalogObject(created.ID(), tableDefinition("final", true))
	require.NoError(t, err)
	afterAlter := catalogForObjects(t, profile, source, starting, alterDefinition)

	createFact, err := NewFact(created.ID(), "$", FactOperatorPresent, "")
	require.NoError(t, err)
	createdName, err := NewFact(created.ID(), "/name", FactOperatorEqual, `"created"`)
	require.NoError(t, err)
	renamedName, err := NewFact(created.ID(), "/name", FactOperatorEqual, `"renamed"`)
	require.NoError(t, err)
	finalName, err := NewFact(created.ID(), "/name", FactOperatorEqual, `"final"`)
	require.NoError(t, err)
	titleFact, err := NewFact(created.ID(), "/columns/1/name", FactOperatorEqual, `"title"`)
	require.NoError(t, err)
	droppedFact, err := NewFact(created.ID(), "$", FactOperatorAbsent, "")
	require.NoError(t, err)
	create := operationFor(t, "create", OperationCreateTable, created.ID(), nil, nil,
		[]Fact{createFact}, afterCreate, "CREATE TABLE created (id INTEGER)")
	rename := operationFor(t, "rename", OperationRenameTable, created.ID(), []OperationID{"create"},
		[]Fact{createdName}, []Fact{renamedName}, afterRename, "ALTER TABLE created RENAME TO renamed")
	renameTwo := operationFor(t, "rename-two", OperationRenameTable, created.ID(), []OperationID{"rename"},
		[]Fact{renamedName}, []Fact{finalName}, afterSecondRename, "ALTER TABLE renamed RENAME TO final")
	alter := operationFor(t, "alter", OperationAddColumn, created.ID(), []OperationID{"rename-two"},
		[]Fact{finalName}, []Fact{titleFact}, afterAlter, "ALTER TABLE final ADD COLUMN title TEXT")
	drop := operationFor(t, "drop", OperationDropTable, created.ID(), []OperationID{"alter"},
		[]Fact{titleFact}, []Fact{droppedFact}, baseline, "DROP TABLE final")
	renameBinding, err := NewBaselineRename("rename", created.ID(), "main", "renamed")
	require.NoError(t, err)
	renameBindingTwo, err := NewBaselineRename("rename-two", created.ID(), "main", "final")
	require.NoError(t, err)
	decisions := []Decision{
		resolvedContractDecision(t, "rename-decision", DecisionRenameObject, created.ID(), "main.created", "main.renamed", ""),
		resolvedContractDecision(t, "rename-two-decision", DecisionRenameObject, created.ID(), "main.renamed", "main.final", ""),
		resolvedContractDecision(t, "drop-decision", DecisionAcceptDestructive, created.ID(), "", "", "approved"),
	}
	operations := []Operation{create, rename, renameTwo, alter, drop}
	catalogs := []Catalog{afterCreate, afterRename, afterSecondRename, afterAlter, baseline}
	steps := make([]ResolvedCatalogStep, 0, len(operations))
	for i, operation := range operations {
		step, stepErr := NewResolvedCatalogStep(operation.ID(), catalogs[i])
		require.NoError(t, stepErr)
		steps = append(steps, step)
	}
	return resolvedContractChain{
		baseline: baseline, steps: steps, operations: operations, decisions: decisions,
		future:  []BaselineObject{created},
		renamed: []BaselineRename{renameBinding, renameBindingTwo}, catalogs: catalogs,
	}
}

func resolveChain(t *testing.T, chain resolvedContractChain, steps []ResolvedCatalogStep, operations []Operation) error {
	t.Helper()
	_, err := NewResolvedChanges(chain.baseline, steps, chain.decisions, operations, chain.future, chain.renamed)
	return err
}

func TestAdjacentResolvedCatalogStepMatrix(t *testing.T) {
	chain := makeResolvedContractChain(t)
	valid := func() []ResolvedCatalogStep {
		return append([]ResolvedCatalogStep(nil), chain.steps...)
	}
	t.Run("valid exact adjacent steps", func(t *testing.T) {
		resolved, err := NewResolvedChanges(chain.baseline, valid(), chain.decisions, chain.operations, chain.future, chain.renamed)
		require.NoError(t, err)
		stored := resolved.CatalogSteps()
		require.Len(t, stored, len(chain.catalogs))
		for i := range stored {
			require.Equal(t, chain.steps[i].Operation(), stored[i].Operation())
			require.Equal(t, chain.catalogs[i].SourceIdentity(), stored[i].Catalog().SourceIdentity())
			require.Equal(t, chain.catalogs[i].physical.Engine, stored[i].Catalog().physical.Engine)
			require.Equal(t, chain.catalogs[i].physical.Objects, stored[i].Catalog().physical.Objects)
			wantDigest, digestErr := CatalogDigest(chain.catalogs[i])
			require.NoError(t, digestErr)
			gotDigest, digestErr := CatalogDigest(stored[i].Catalog())
			require.NoError(t, digestErr)
			require.Equal(t, wantDigest, gotDigest)
		}
	})
	t.Run("nil steps", func(t *testing.T) {
		err := resolveChain(t, chain, nil, chain.operations)
		require.ErrorIs(t, err, ErrInvalidPlan)
		require.ErrorContains(t, err, "resolved catalog steps must be nonnil")
	})
	t.Run("missing tail", func(t *testing.T) {
		err := resolveChain(t, chain, chain.steps[:4], chain.operations)
		require.ErrorIs(t, err, ErrInvalidPlan)
		require.ErrorContains(t, err, "step count does not match operations")
	})
	t.Run("extra tail", func(t *testing.T) {
		extra, err := NewResolvedCatalogStep("extra", chain.baseline)
		require.NoError(t, err)
		steps := append(valid(), extra)
		err = resolveChain(t, chain, steps, chain.operations)
		require.ErrorIs(t, err, ErrInvalidPlan)
		require.ErrorContains(t, err, "step count does not match operations")
	})
	t.Run("sparse middle", func(t *testing.T) {
		extra, err := NewResolvedCatalogStep("filler", chain.baseline)
		require.NoError(t, err)
		steps := []ResolvedCatalogStep{chain.steps[0], chain.steps[1], chain.steps[3], chain.steps[4], extra}
		err = resolveChain(t, chain, steps, chain.operations)
		require.ErrorIs(t, err, ErrInvalidPlan)
		require.ErrorContains(t, err, "not in stable order")
	})
	t.Run("reordered adjacent", func(t *testing.T) {
		steps := valid()
		steps[1], steps[2] = steps[2], steps[1]
		err := resolveChain(t, chain, steps, chain.operations)
		require.ErrorIs(t, err, ErrInvalidPlan)
		require.ErrorContains(t, err, "not in stable order")
	})
	t.Run("duplicate valid step", func(t *testing.T) {
		steps := valid()
		steps[2] = steps[1]
		err := resolveChain(t, chain, steps, chain.operations)
		require.ErrorIs(t, err, ErrInvalidPlan)
		require.ErrorContains(t, err, "not in stable order")
	})
	t.Run("wrong operation", func(t *testing.T) {
		wrong, err := NewResolvedCatalogStep("other", chain.steps[2].Catalog())
		require.NoError(t, err)
		steps := valid()
		steps[2] = wrong
		err = resolveChain(t, chain, steps, chain.operations)
		require.ErrorIs(t, err, ErrInvalidPlan)
		require.ErrorContains(t, err, "not in stable order")
	})
	t.Run("engine drift", func(t *testing.T) {
		value, err := engineprofile.Builtin("mysql-8.4", engineprofile.Version{Known: true, Major: 8, Minor: 4})
		require.NoError(t, err)
		profile, err := NewProfile(resolvedContractProfileSource{value: value})
		require.NoError(t, err)
		objects := []CatalogObject{}
		for _, name := range []string{"users", "final"} {
			id := ObjectID("ordinary")
			if name == "final" {
				id = chain.future[0].ID()
			}
			object, objectErr := NewCatalogObject(id, tableDefinition(name, false))
			require.NoError(t, objectErr)
			objects = append(objects, object)
		}
		catalog := catalogForObjects(t, profile, chain.baseline.SourceIdentity(), objects...)
		steps := valid()
		steps[2], err = NewResolvedCatalogStep(chain.steps[2].Operation(), catalog)
		require.NoError(t, err)
		err = resolveChain(t, chain, steps, chain.operations)
		require.ErrorIs(t, err, ErrInvalidPlan)
		require.ErrorContains(t, err, "resolved catalog identity differs")
	})
	t.Run("source drift", func(t *testing.T) {
		objects := []CatalogObject{}
		for _, name := range []string{"users", "final"} {
			id := ObjectID("ordinary")
			if name == "final" {
				id = chain.future[0].ID()
			}
			object, objectErr := NewCatalogObject(id, tableDefinition(name, false))
			require.NoError(t, objectErr)
			objects = append(objects, object)
		}
		catalog := catalogForObjects(t, resolvedContractProfile(t), "other-source", objects...)
		steps := valid()
		var err error
		steps[2], err = NewResolvedCatalogStep(chain.steps[2].Operation(), catalog)
		require.NoError(t, err)
		err = resolveChain(t, chain, steps, chain.operations)
		require.ErrorIs(t, err, ErrInvalidPlan)
		require.ErrorContains(t, err, "resolved catalog identity differs")
	})
	t.Run("precondition uses baseline", func(t *testing.T) {
		err := EvaluateFact(chain.baseline, chain.operations[2].Preconditions()[0])
		require.ErrorIs(t, err, ErrFactMismatch)
	})
	t.Run("precondition uses final", func(t *testing.T) {
		err := EvaluateFact(chain.catalogs[3], chain.operations[2].Preconditions()[0])
		require.ErrorIs(t, err, ErrFactMismatch)
	})
	t.Run("postcondition uses baseline", func(t *testing.T) {
		err := EvaluateFact(chain.baseline, chain.operations[2].Postconditions()[0])
		require.ErrorIs(t, err, ErrFactMismatch)
	})
	t.Run("postcondition uses final", func(t *testing.T) {
		err := EvaluateFact(chain.catalogs[3], chain.operations[1].Postconditions()[0])
		require.ErrorIs(t, err, ErrFactMismatch)
	})
}

func TestOccurrenceIdentityContract(t *testing.T) {
	profile := resolvedContractProfile(t)
	ordinaryID := ObjectID("e3806f3aad59387841fa56cf1ca8e420ee606c129e61c92aedf4fb145558941e")
	definitions := map[string]schema.TableDef{
		"a": tableDefinition("a", false),
		"b": tableDefinition("b", false),
	}
	publicA := definitions["a"]
	publicA.Schema = "public"
	objects := map[string]ObjectID{
		"op-create-a-1": "6ceee67869f9f7832b830bd9efa4038220522c5a5eeb914e2fc966df36f8368b",
		"op-create-a-2": "d87e025df00008ef5ff2b04a6322e8c5fd673ecccb7a46ed6df48b064b0b4c5f",
		"op-create-a-3": "a286e83ef6f1e6d2ccde9d882d9059023290840de6cc090beedc3e59ef58e7dd",
	}
	for operationID, wantID := range objects {
		object, err := NewIntroducedBaselineObject("fixture-source", OperationID(operationID), publicA)
		require.NoError(t, err)
		require.Equal(t, wantID, object.ID())
		require.Equal(t, OperationID(operationID), object.IntroducedBy())
		require.Len(t, string(object.ID()), 64)
	}
	ordinary, err := NewBaselineObject(ordinaryID, "table", "public", "a")
	require.NoError(t, err)
	require.Empty(t, ordinary.IntroducedBy())

	createFuture, err := NewIntroducedBaselineObject("fixture-source", "op-create-a-1", publicA)
	require.NoError(t, err)
	ordinaryObject, err := NewCatalogObject(ordinary.ID(), publicA)
	require.NoError(t, err)
	unrelatedDefinition := definitions["a"]
	unrelatedDefinition.Schema = "public"
	unrelatedDefinition.Name = "users"
	unrelatedObject, err := NewCatalogObject("unrelated-start", unrelatedDefinition)
	require.NoError(t, err)
	createdObject, err := NewCatalogObject(createFuture.ID(), publicA)
	require.NoError(t, err)
	baseline := catalogForObjects(t, profile, "fixture-source", ordinaryObject, unrelatedObject)
	renameDefinition := definitions["b"]
	renameDefinition.Schema = "public"
	renameObject, err := NewCatalogObject(ordinary.ID(), renameDefinition)
	require.NoError(t, err)
	withRename := catalogForObjects(t, profile, "fixture-source", renameObject, unrelatedObject)
	withCreate := catalogForObjects(t, profile, "fixture-source", renameObject, createdObject, unrelatedObject)
	gotID, ok := withRename.ObjectID(schema.ObjectTable, "public", "b")
	require.True(t, ok)
	require.Equal(t, ordinaryID, gotID)
	_, ok = withRename.ObjectID(schema.ObjectTable, "public", "a")
	require.False(t, ok)
	gotID, ok = withCreate.ObjectID(schema.ObjectTable, "public", "a")
	require.True(t, ok)
	require.Equal(t, createFuture.ID(), gotID)
	requireActiveCatalog(t, baseline, map[string]ObjectID{"public.a": ordinaryID, "public.users": unrelatedObject.ID()}, map[ObjectID]string{createFuture.ID(): "public.a"})
	requireActiveCatalog(t, withRename, map[string]ObjectID{"public.b": ordinaryID, "public.users": unrelatedObject.ID()}, map[ObjectID]string{createFuture.ID(): "public.a"})
	requireActiveCatalog(t, withCreate, map[string]ObjectID{"public.b": ordinaryID, "public.a": createFuture.ID(), "public.users": unrelatedObject.ID()}, nil)
	rename, err := operationForOccurrence(t, "rename-a-b", OperationRenameTable, ordinary.ID(), nil, withRename, "ALTER TABLE a RENAME TO b")
	require.NoError(t, err)
	create, err := operationForOccurrence(t, "op-create-a-1", OperationCreateTable, createFuture.ID(), []OperationID{"rename-a-b"}, withCreate, "CREATE TABLE a (id INTEGER)")
	require.NoError(t, err)
	renameBinding, err := NewBaselineRename(rename.ID(), ordinary.ID(), "public", "b")
	require.NoError(t, err)
	decisions := []Decision{resolvedContractDecision(t, "rename-a-b-decision", DecisionRenameObject, ordinary.ID(), "public.a", "public.b", "")}
	step1, err := NewResolvedCatalogStep(rename.ID(), withRename)
	require.NoError(t, err)
	step2, err := NewResolvedCatalogStep(create.ID(), withCreate)
	require.NoError(t, err)
	resolved, err := NewResolvedChanges(baseline, []ResolvedCatalogStep{step1, step2}, decisions,
		[]Operation{rename, create}, []BaselineObject{createFuture}, []BaselineRename{renameBinding})
	require.NoError(t, err)
	stored := resolved.CatalogSteps()
	require.Len(t, stored, 2)
	requireActiveCatalog(t, resolved.BaselineCatalog(), map[string]ObjectID{"public.a": ordinaryID, "public.users": unrelatedObject.ID()}, map[ObjectID]string{createFuture.ID(): "public.a"})
	requireActiveCatalog(t, stored[0].Catalog(), map[string]ObjectID{"public.b": ordinaryID, "public.users": unrelatedObject.ID()}, map[ObjectID]string{createFuture.ID(): "public.a"})
	requireActiveCatalog(t, stored[1].Catalog(), map[string]ObjectID{"public.b": ordinaryID, "public.a": createFuture.ID(), "public.users": unrelatedObject.ID()}, nil)
	requireActiveCatalog(t, resolved.TargetCatalog(), map[string]ObjectID{"public.b": ordinaryID, "public.a": createFuture.ID(), "public.users": unrelatedObject.ID()}, nil)
	require.Equal(t, baseline.physical, resolved.BaselineCatalog().physical)
	require.Equal(t, withRename.physical, stored[0].Catalog().physical)
	require.Equal(t, withCreate.physical, stored[1].Catalog().physical)
	require.Equal(t, withCreate.physical, resolved.TargetCatalog().physical)
}

func TestOccurrenceReuseSequences(t *testing.T) {
	profile := resolvedContractProfile(t)
	var gotID ObjectID
	var ok bool
	definition := tableDefinition("a", false)
	definition.Schema = "public"
	ordinaryID := ObjectID("e3806f3aad59387841fa56cf1ca8e420ee606c129e61c92aedf4fb145558941e")
	ordinaryObject, err := NewCatalogObject(ordinaryID, definition)
	require.NoError(t, err)
	unrelatedDefinition := definition
	unrelatedDefinition.Name = "users"
	unrelatedObject, err := NewCatalogObject("unrelated-start-2", unrelatedDefinition)
	require.NoError(t, err)
	ordinaryBaseline := catalogForObjects(t, profile, "fixture-source", ordinaryObject, unrelatedObject)

	futureTwo, err := NewIntroducedBaselineObject("fixture-source", "op-create-a-2", definition)
	require.NoError(t, err)
	futureTwoObject, err := NewCatalogObject(futureTwo.ID(), definition)
	require.NoError(t, err)
	emptyReuse := []CatalogObject{unrelatedObject}
	dropCatalog := catalogForObjects(t, profile, "fixture-source", emptyReuse...)
	createCatalog := catalogForObjects(t, profile, "fixture-source", futureTwoObject, unrelatedObject)
	dropOrdinary, err := operationForOccurrence(t, "drop-ordinary", OperationDropTable, ordinaryObject.ID(), nil, dropCatalog, "DROP TABLE a")
	require.NoError(t, err)
	createTwo, err := operationForOccurrence(t, "op-create-a-2", OperationCreateTable, futureTwo.ID(), []OperationID{"drop-ordinary"}, createCatalog, "CREATE TABLE a (id INTEGER)")
	require.NoError(t, err)
	dropDecision := resolvedContractDecision(t, "drop-ordinary-decision", DecisionAcceptDestructive, ordinaryObject.ID(), "", "", "approved")
	dropStep, err := NewResolvedCatalogStep(dropOrdinary.ID(), dropCatalog)
	require.NoError(t, err)
	createStep, err := NewResolvedCatalogStep(createTwo.ID(), createCatalog)
	require.NoError(t, err)
	_, ok = dropCatalog.ObjectID(schema.ObjectTable, "public", "a")
	require.False(t, ok)
	gotID, ok = createStep.Catalog().ObjectID(schema.ObjectTable, "public", "a")
	require.True(t, ok)
	require.Equal(t, futureTwo.ID(), gotID)
	requireActiveCatalog(t, ordinaryBaseline, map[string]ObjectID{"public.a": ordinaryID, "public.users": unrelatedObject.ID()}, map[ObjectID]string{futureTwo.ID(): "public.a"})
	requireActiveCatalog(t, dropStep.Catalog(), map[string]ObjectID{"public.users": unrelatedObject.ID()}, map[ObjectID]string{ordinaryID: "public.a", futureTwo.ID(): "public.a"})
	requireActiveCatalog(t, createStep.Catalog(), map[string]ObjectID{"public.a": futureTwo.ID(), "public.users": unrelatedObject.ID()}, map[ObjectID]string{ordinaryID: "public.a"})
	resolved, err := NewResolvedChanges(ordinaryBaseline, []ResolvedCatalogStep{dropStep, createStep}, []Decision{dropDecision}, []Operation{dropOrdinary, createTwo}, []BaselineObject{futureTwo}, []BaselineRename{})
	require.NoError(t, err)
	stored := resolved.CatalogSteps()
	require.Len(t, stored, 2)
	requireActiveCatalog(t, resolved.BaselineCatalog(), map[string]ObjectID{"public.a": ordinaryID, "public.users": unrelatedObject.ID()}, map[ObjectID]string{futureTwo.ID(): "public.a"})
	requireActiveCatalog(t, stored[0].Catalog(), map[string]ObjectID{"public.users": unrelatedObject.ID()}, map[ObjectID]string{ordinaryID: "public.a", futureTwo.ID(): "public.a"})
	requireActiveCatalog(t, stored[1].Catalog(), map[string]ObjectID{"public.a": futureTwo.ID(), "public.users": unrelatedObject.ID()}, map[ObjectID]string{ordinaryID: "public.a"})
	requireActiveCatalog(t, resolved.TargetCatalog(), map[string]ObjectID{"public.a": futureTwo.ID(), "public.users": unrelatedObject.ID()}, map[ObjectID]string{ordinaryID: "public.a"})
	require.Equal(t, ordinaryBaseline.physical, resolved.BaselineCatalog().physical)
	require.Equal(t, dropCatalog.physical, stored[0].Catalog().physical)
	require.Equal(t, createCatalog.physical, stored[1].Catalog().physical)
	require.Equal(t, createCatalog.physical, resolved.TargetCatalog().physical)

	futureOne, err := NewIntroducedBaselineObject("fixture-source", "op-create-a-1", definition)
	require.NoError(t, err)
	futureThree, err := NewIntroducedBaselineObject("fixture-source", "op-create-a-3", definition)
	require.NoError(t, err)
	futureOneObject, err := NewCatalogObject(futureOne.ID(), definition)
	require.NoError(t, err)
	futureThreeObject, err := NewCatalogObject(futureThree.ID(), definition)
	require.NoError(t, err)
	unrelatedThreeDefinition := definition
	unrelatedThreeDefinition.Name = "users"
	unrelatedThreeObject, err := NewCatalogObject("unrelated-start-3", unrelatedThreeDefinition)
	require.NoError(t, err)
	empty := []CatalogObject{unrelatedThreeObject}
	emptyBaseline := catalogForObjects(t, profile, "fixture-source", empty...)
	createOneCatalog := catalogForObjects(t, profile, "fixture-source", futureOneObject, unrelatedThreeObject)
	createThreeCatalog := catalogForObjects(t, profile, "fixture-source", futureThreeObject, unrelatedThreeObject)
	createOne, err := operationForOccurrence(t, "op-create-a-1", OperationCreateTable, futureOne.ID(), nil, createOneCatalog, "CREATE TABLE a (id INTEGER)")
	require.NoError(t, err)
	dropOne, err := operationForOccurrence(t, "drop-a-1", OperationDropTable, futureOne.ID(), []OperationID{"op-create-a-1"}, emptyBaseline, "DROP TABLE a")
	require.NoError(t, err)
	createThree, err := operationForOccurrence(t, "op-create-a-3", OperationCreateTable, futureThree.ID(), []OperationID{"drop-a-1"}, createThreeCatalog, "CREATE TABLE a (id INTEGER)")
	require.NoError(t, err)
	stepOne, err := NewResolvedCatalogStep(createOne.ID(), createOneCatalog)
	require.NoError(t, err)
	stepDrop, err := NewResolvedCatalogStep(dropOne.ID(), emptyBaseline)
	require.NoError(t, err)
	stepThree, err := NewResolvedCatalogStep(createThree.ID(), createThreeCatalog)
	require.NoError(t, err)
	gotID, ok = stepOne.Catalog().ObjectID(schema.ObjectTable, "public", "a")
	require.True(t, ok)
	require.Equal(t, futureOne.ID(), gotID)
	_, ok = stepDrop.Catalog().ObjectID(schema.ObjectTable, "public", "a")
	require.False(t, ok)
	gotID, ok = stepThree.Catalog().ObjectID(schema.ObjectTable, "public", "a")
	require.True(t, ok)
	require.Equal(t, futureThree.ID(), gotID)
	requireActiveCatalog(t, emptyBaseline, map[string]ObjectID{"public.users": unrelatedThreeObject.ID()}, map[ObjectID]string{futureOne.ID(): "public.a", futureThree.ID(): "public.a"})
	requireActiveCatalog(t, stepOne.Catalog(), map[string]ObjectID{"public.a": futureOne.ID(), "public.users": unrelatedThreeObject.ID()}, map[ObjectID]string{futureThree.ID(): "public.a"})
	requireActiveCatalog(t, stepDrop.Catalog(), map[string]ObjectID{"public.users": unrelatedThreeObject.ID()}, map[ObjectID]string{futureOne.ID(): "public.a", futureThree.ID(): "public.a"})
	requireActiveCatalog(t, stepThree.Catalog(), map[string]ObjectID{"public.a": futureThree.ID(), "public.users": unrelatedThreeObject.ID()}, map[ObjectID]string{futureOne.ID(): "public.a"})
	dropDecisionOne := resolvedContractDecision(t, "drop-a-1-decision", DecisionAcceptDestructive, futureOne.ID(), "", "", "approved")
	resolvedThree, err := NewResolvedChanges(emptyBaseline, []ResolvedCatalogStep{stepOne, stepDrop, stepThree}, []Decision{dropDecisionOne}, []Operation{createOne, dropOne, createThree}, []BaselineObject{futureOne, futureThree}, []BaselineRename{})
	require.NoError(t, err)
	storedThree := resolvedThree.CatalogSteps()
	require.Len(t, storedThree, 3)
	requireActiveCatalog(t, resolvedThree.BaselineCatalog(), map[string]ObjectID{"public.users": unrelatedThreeObject.ID()}, map[ObjectID]string{futureOne.ID(): "public.a", futureThree.ID(): "public.a"})
	requireActiveCatalog(t, storedThree[0].Catalog(), map[string]ObjectID{"public.a": futureOne.ID(), "public.users": unrelatedThreeObject.ID()}, map[ObjectID]string{futureThree.ID(): "public.a"})
	requireActiveCatalog(t, storedThree[1].Catalog(), map[string]ObjectID{"public.users": unrelatedThreeObject.ID()}, map[ObjectID]string{futureOne.ID(): "public.a", futureThree.ID(): "public.a"})
	requireActiveCatalog(t, storedThree[2].Catalog(), map[string]ObjectID{"public.a": futureThree.ID(), "public.users": unrelatedThreeObject.ID()}, map[ObjectID]string{futureOne.ID(): "public.a"})
	requireActiveCatalog(t, resolvedThree.TargetCatalog(), map[string]ObjectID{"public.a": futureThree.ID(), "public.users": unrelatedThreeObject.ID()}, map[ObjectID]string{futureOne.ID(): "public.a"})
	require.Equal(t, emptyBaseline.physical, resolvedThree.BaselineCatalog().physical)
	require.Equal(t, createOneCatalog.physical, storedThree[0].Catalog().physical)
	require.Equal(t, emptyBaseline.physical, storedThree[1].Catalog().physical)
	require.Equal(t, createThreeCatalog.physical, storedThree[2].Catalog().physical)
	require.Equal(t, createThreeCatalog.physical, resolvedThree.TargetCatalog().physical)
	require.Equal(t, ObjectID("6ceee67869f9f7832b830bd9efa4038220522c5a5eeb914e2fc966df36f8368b"), futureOne.ID())
	require.Equal(t, ObjectID("d87e025df00008ef5ff2b04a6322e8c5fd673ecccb7a46ed6df48b064b0b4c5f"), futureTwo.ID())
	require.Equal(t, ObjectID("a286e83ef6f1e6d2ccde9d882d9059023290840de6cc090beedc3e59ef58e7dd"), futureThree.ID())
}

func TestResolvedPrefixNegativeContract(t *testing.T) {
	profile := resolvedContractProfile(t)
	object, err := NewCatalogObject("ordinary", tableDefinition("users", false))
	require.NoError(t, err)
	baseline := catalogForObjects(t, profile, "negative-source", object)
	operation, err := operationForOccurrence(t, "native", OperationNativeSQL, object.ID(), nil, baseline, "SELECT 1")
	require.NoError(t, err)
	decision := resolvedContractDecision(t, "native-decision", DecisionAcceptNativeSQL, object.ID(), "", "", "approved")
	wrongStep, err := NewResolvedCatalogStep("different-operation", baseline)
	require.NoError(t, err)
	_, err = NewResolvedChanges(baseline, []ResolvedCatalogStep{wrongStep}, []Decision{decision}, []Operation{operation}, nil, nil)
	require.ErrorIs(t, err, ErrInvalidPlan)
	require.ErrorContains(t, err, "stable order")
	_, err = NewResolvedChanges(baseline, nil, []Decision{decision}, []Operation{operation}, []BaselineObject{}, []BaselineRename{})
	require.ErrorIs(t, err, ErrInvalidPlan)
	require.ErrorContains(t, err, "must be nonnil")
	extraStep, err := NewResolvedCatalogStep("extra", baseline)
	require.NoError(t, err)
	_, err = NewResolvedChanges(baseline, []ResolvedCatalogStep{wrongStep, extraStep}, []Decision{decision}, []Operation{operation}, nil, nil)
	require.ErrorIs(t, err, ErrInvalidPlan)
	require.ErrorContains(t, err, "step count")
	duplicateOperation, err := operationForOccurrence(t, "duplicate", OperationNativeSQL, object.ID(), []OperationID{"native"}, baseline, "SELECT 2")
	require.NoError(t, err)
	duplicateStep, err := NewResolvedCatalogStep(duplicateOperation.ID(), baseline)
	require.NoError(t, err)
	duplicateDecision := resolvedContractDecision(t, "duplicate-decision", DecisionAcceptNativeSQL, object.ID(), "", "", "approved")
	_, err = NewResolvedChanges(baseline, []ResolvedCatalogStep{wrongStep, duplicateStep}, []Decision{decision, duplicateDecision}, []Operation{operation, duplicateOperation}, nil, nil)
	require.ErrorIs(t, err, ErrInvalidPlan)
	require.ErrorContains(t, err, "stable order")
	otherProfile := resolvedContractProfile(t)
	otherObject, err := NewCatalogObject("ordinary", tableDefinition("users", false))
	require.NoError(t, err)
	otherBaseline := catalogForObjects(t, otherProfile, "other-source", otherObject)
	otherStep, err := NewResolvedCatalogStep(operation.ID(), otherBaseline)
	require.NoError(t, err)
	_, err = NewResolvedChanges(baseline, []ResolvedCatalogStep{otherStep}, []Decision{decision}, []Operation{operation}, []BaselineObject{}, []BaselineRename{})
	require.ErrorIs(t, err, ErrInvalidPlan)
	require.ErrorContains(t, err, "identity differs")
}

func TestResolvedPrefixNamedFailures(t *testing.T) {
	profile := resolvedContractProfile(t)
	definition := tableDefinition("a", false)
	definition.Schema = "public"
	ordinaryID := ObjectID("e3806f3aad59387841fa56cf1ca8e420ee606c129e61c92aedf4fb145558941e")
	newObject := func(t *testing.T, id ObjectID, definition schema.TableDef) CatalogObject {
		t.Helper()
		object, err := NewCatalogObject(id, definition)
		require.NoError(t, err)
		return object
	}
	newStep := func(t *testing.T, operation Operation, catalog Catalog) ResolvedCatalogStep {
		t.Helper()
		step, err := NewResolvedCatalogStep(operation.ID(), catalog)
		require.NoError(t, err)
		return step
	}
	newPlanErr := func(t *testing.T, baseline Catalog, steps []ResolvedCatalogStep, decisions []Decision, operations []Operation, future []BaselineObject, renames []BaselineRename) error {
		t.Helper()
		_, err := NewResolvedChanges(baseline, steps, decisions, operations, future, renames)
		return err
	}

	t.Run("wrong future ID", func(t *testing.T) {
		future, err := NewIntroducedBaselineObject("negative-source", "create", definition)
		require.NoError(t, err)
		baseline := catalogForObjects(t, profile, "negative-source", []CatalogObject{}...)
		originalID := future.ID()
		after := catalogForObjects(t, profile, "negative-source", newObject(t, originalID, definition))
		operation := operationFor(t, "create", OperationCreateTable, originalID, nil, nil, nil, after, "CREATE TABLE a (id INTEGER)")
		future.id = ObjectID("wrong-future-id")
		err = newPlanErr(t, baseline, []ResolvedCatalogStep{newStep(t, operation, after)}, nil,
			[]Operation{operation}, []BaselineObject{future}, nil)
		require.ErrorIs(t, err, ErrInvalidIdentity)
		require.True(t, strings.Contains(err.Error(), "invalid deterministic ID") ||
			strings.Contains(err.Error(), "no matching create operation") ||
			strings.Contains(err.Error(), "does not name its create_table operation"))
	})

	t.Run("not yet created use", func(t *testing.T) {
		future, err := NewIntroducedBaselineObject("negative-source", "create", definition)
		require.NoError(t, err)
		baseline := catalogForObjects(t, profile, "negative-source", []CatalogObject{}...)
		baselineAfter := catalogForObjects(t, profile, "negative-source", []CatalogObject{}...)
		created := catalogForObjects(t, profile, "negative-source", newObject(t, future.ID(), definition))
		native := operationFor(t, "native", OperationNativeSQL, future.ID(), nil, nil, nil, baselineAfter, "SELECT 1")
		create := operationFor(t, "create", OperationCreateTable, future.ID(), []OperationID{"native"}, nil, nil, created, "CREATE TABLE a (id INTEGER)")
		decision := resolvedContractDecision(t, "native-decision", DecisionAcceptNativeSQL, future.ID(), "", "", "approved")
		err = newPlanErr(t, baseline, []ResolvedCatalogStep{newStep(t, native, baselineAfter), newStep(t, create, created)},
			[]Decision{decision}, []Operation{native, create}, []BaselineObject{future}, nil)
		require.ErrorIs(t, err, ErrInvalidIdentity)
		require.ErrorContains(t, err, "references inactive object")
	})

	t.Run("premature rename", func(t *testing.T) {
		future, err := NewIntroducedBaselineObject("negative-source", "create", definition)
		require.NoError(t, err)
		baseline := catalogForObjects(t, profile, "negative-source", []CatalogObject{}...)
		created := catalogForObjects(t, profile, "negative-source", newObject(t, future.ID(), definition))
		rename, err := operationForOccurrence(t, "rename", OperationRenameTable, future.ID(), nil, created, "ALTER TABLE a RENAME TO b")
		require.NoError(t, err)
		create := operationFor(t, "create", OperationCreateTable, future.ID(), []OperationID{"rename"}, nil, nil, created, "CREATE TABLE a (id INTEGER)")
		binding, err := NewBaselineRename("rename", future.ID(), "public", "b")
		require.NoError(t, err)
		decision := resolvedContractDecision(t, "rename-decision", DecisionRenameObject, future.ID(), "public.a", "public.b", "")
		err = newPlanErr(t, baseline, []ResolvedCatalogStep{newStep(t, rename, created), newStep(t, create, created)},
			[]Decision{decision}, []Operation{rename, create}, []BaselineObject{future}, []BaselineRename{binding})
		require.ErrorIs(t, err, ErrInvalidDecision)
		require.ErrorContains(t, err, "inactive")
	})

	t.Run("resurrected dropped object", func(t *testing.T) {
		object := newObject(t, ordinaryID, definition)
		baseline := catalogForObjects(t, profile, "negative-source", object)
		empty := catalogForObjects(t, profile, "negative-source", []CatalogObject{}...)
		drop, err := operationForOccurrence(t, "drop", OperationDropTable, ordinaryID, nil, empty, "DROP TABLE a")
		require.NoError(t, err)
		native, err := operationForOccurrence(t, "native", OperationNativeSQL, ordinaryID, []OperationID{"drop"}, empty, "SELECT 1")
		require.NoError(t, err)
		dropDecision := resolvedContractDecision(t, "drop-decision", DecisionAcceptDestructive, ordinaryID, "", "", "approved")
		nativeDecision := resolvedContractDecision(t, "native-decision", DecisionAcceptNativeSQL, ordinaryID, "", "", "approved")
		err = newPlanErr(t, baseline, []ResolvedCatalogStep{newStep(t, drop, empty), newStep(t, native, empty)},
			[]Decision{dropDecision, nativeDecision}, []Operation{drop, native}, nil, nil)
		require.ErrorIs(t, err, ErrInvalidIdentity)
		require.ErrorContains(t, err, "references inactive object")
	})

	t.Run("old ID reuse", func(t *testing.T) {
		future, err := NewIntroducedBaselineObject("negative-source", "op-create-a-2", definition)
		require.NoError(t, err)
		ordinary := newObject(t, ordinaryID, definition)
		unrelatedDefinition := definition
		unrelatedDefinition.Name = "users"
		unrelated := newObject(t, "unrelated-old-id", unrelatedDefinition)
		baseline := catalogForObjects(t, profile, "negative-source", ordinary, unrelated)
		dropCatalog := catalogForObjects(t, profile, "negative-source", unrelated)
		after := catalogForObjects(t, profile, "negative-source", newObject(t, future.ID(), definition), unrelated)
		drop, err := operationForOccurrence(t, "drop-ordinary", OperationDropTable, ordinaryID, nil, dropCatalog, "DROP TABLE a")
		require.NoError(t, err)
		create := operationFor(t, "op-create-a-2", OperationCreateTable, ordinaryID, []OperationID{"drop-ordinary"}, nil, nil, after, "CREATE TABLE a (id INTEGER)")
		err = newPlanErr(t, baseline, []ResolvedCatalogStep{newStep(t, drop, dropCatalog), newStep(t, create, after)},
			[]Decision{resolvedContractDecision(t, "drop-decision", DecisionAcceptDestructive, ordinaryID, "", "", "approved")},
			[]Operation{drop, create}, []BaselineObject{future}, nil)
		require.ErrorIs(t, err, ErrInvalidIdentity)
		require.ErrorContains(t, err, "future object")
	})

	t.Run("active create destination collision", func(t *testing.T) {
		ordinary := newObject(t, ordinaryID, definition)
		future, err := NewIntroducedBaselineObject("negative-source", "create", definition)
		require.NoError(t, err)
		baseline := catalogForObjects(t, profile, "negative-source", ordinary)
		create := operationFor(t, "create", OperationCreateTable, future.ID(), nil, nil, nil, baseline, "CREATE TABLE a (id INTEGER)")
		err = newPlanErr(t, baseline, []ResolvedCatalogStep{newStep(t, create, baseline)}, nil,
			[]Operation{create}, []BaselineObject{future}, nil)
		require.ErrorIs(t, err, ErrInvalidIdentity)
		require.ErrorContains(t, err, "create destination collision")
	})

	t.Run("active rename destination collision", func(t *testing.T) {
		otherDefinition := tableDefinition("b", false)
		otherDefinition.Schema = "public"
		ordinary := newObject(t, ordinaryID, definition)
		other := newObject(t, "other", otherDefinition)
		baseline := catalogForObjects(t, profile, "negative-source", ordinary, other)
		rename, err := operationForOccurrence(t, "rename", OperationRenameTable, ordinaryID, nil, baseline, "ALTER TABLE a RENAME TO b")
		require.NoError(t, err)
		binding, err := NewBaselineRename("rename", ordinaryID, "public", "b")
		require.NoError(t, err)
		decision := resolvedContractDecision(t, "rename-decision", DecisionRenameObject, ordinaryID, "public.a", "public.b", "")
		err = newPlanErr(t, baseline, []ResolvedCatalogStep{newStep(t, rename, baseline)}, []Decision{decision},
			[]Operation{rename}, nil, []BaselineRename{binding})
		require.ErrorIs(t, err, ErrInvalidIdentity)
		require.ErrorContains(t, err, "rename destination collision")
	})

	t.Run("stale rename", func(t *testing.T) {
		ordinary := newObject(t, ordinaryID, definition)
		baseline := catalogForObjects(t, profile, "negative-source", ordinary)
		firstDefinition := definition
		firstDefinition.Name = "b"
		secondDefinition := definition
		secondDefinition.Name = "c"
		firstCatalog := catalogForObjects(t, profile, "negative-source", newObject(t, ordinaryID, firstDefinition))
		secondCatalog := catalogForObjects(t, profile, "negative-source", newObject(t, ordinaryID, secondDefinition))
		first, err := operationForOccurrence(t, "rename-one", OperationRenameTable, ordinaryID, nil, firstCatalog, "ALTER TABLE a RENAME TO b")
		require.NoError(t, err)
		second, err := operationForOccurrence(t, "rename-two", OperationRenameTable, ordinaryID, []OperationID{"rename-one"}, secondCatalog, "ALTER TABLE b RENAME TO c")
		require.NoError(t, err)
		firstBinding, err := NewBaselineRename("rename-one", ordinaryID, "public", "b")
		require.NoError(t, err)
		secondBinding, err := NewBaselineRename("rename-two", ordinaryID, "public", "c")
		require.NoError(t, err)
		firstDecision := resolvedContractDecision(t, "rename-one-decision", DecisionRenameObject, ordinaryID, "public.a", "public.b", "")
		staleDecision := resolvedContractDecision(t, "rename-two-decision", DecisionRenameObject, ordinaryID, "public.a", "public.c", "")
		err = newPlanErr(t, baseline, []ResolvedCatalogStep{newStep(t, first, firstCatalog), newStep(t, second, secondCatalog)},
			[]Decision{firstDecision, staleDecision}, []Operation{first, second}, nil,
			[]BaselineRename{firstBinding, secondBinding})
		require.ErrorIs(t, err, ErrInvalidDecision)
		require.ErrorContains(t, err, "has no matching decision")
	})

	t.Run("engine drift", func(t *testing.T) {
		ordinary := newObject(t, ordinaryID, definition)
		baseline := catalogForObjects(t, profile, "negative-source", ordinary)
		value, err := engineprofile.Builtin("mysql-8.4", engineprofile.Version{Known: true, Major: 8, Minor: 4})
		require.NoError(t, err)
		otherProfile, err := NewProfile(resolvedContractProfileSource{value: value})
		require.NoError(t, err)
		drifted := catalogForObjects(t, otherProfile, "negative-source", ordinary)
		native, err := operationForOccurrence(t, "native", OperationNativeSQL, ordinaryID, nil, drifted, "SELECT 1")
		require.NoError(t, err)
		decision := resolvedContractDecision(t, "native-decision", DecisionAcceptNativeSQL, ordinaryID, "", "", "approved")
		err = newPlanErr(t, baseline, []ResolvedCatalogStep{newStep(t, native, drifted)}, []Decision{decision},
			[]Operation{native}, nil, nil)
		require.ErrorIs(t, err, ErrInvalidPlan)
		require.ErrorContains(t, err, "resolved catalog identity differs")
	})

	t.Run("source drift", func(t *testing.T) {
		ordinary := newObject(t, ordinaryID, definition)
		baseline := catalogForObjects(t, profile, "negative-source", ordinary)
		drifted := catalogForObjects(t, profile, "other-source", ordinary)
		native, err := operationForOccurrence(t, "native", OperationNativeSQL, ordinaryID, nil, drifted, "SELECT 1")
		require.NoError(t, err)
		decision := resolvedContractDecision(t, "native-decision", DecisionAcceptNativeSQL, ordinaryID, "", "", "approved")
		err = newPlanErr(t, baseline, []ResolvedCatalogStep{newStep(t, native, drifted)}, []Decision{decision},
			[]Operation{native}, nil, nil)
		require.ErrorIs(t, err, ErrInvalidPlan)
		require.ErrorContains(t, err, "resolved catalog identity differs")
	})

	t.Run("object ID drift", func(t *testing.T) {
		ordinary := newObject(t, ordinaryID, definition)
		baseline := catalogForObjects(t, profile, "negative-source", ordinary)
		drifted := catalogForObjects(t, profile, "negative-source", newObject(t, "drifted-id", definition))
		native, err := operationForOccurrence(t, "native", OperationNativeSQL, ordinaryID, nil, drifted, "SELECT 1")
		require.NoError(t, err)
		decision := resolvedContractDecision(t, "native-decision", DecisionAcceptNativeSQL, ordinaryID, "", "", "approved")
		err = newPlanErr(t, baseline, []ResolvedCatalogStep{newStep(t, native, drifted)}, []Decision{decision},
			[]Operation{native}, nil, nil)
		require.ErrorIs(t, err, ErrInvalidPlan)
		require.ErrorContains(t, err, "catalog occurrence")
	})

	t.Run("changed introduced_by", func(t *testing.T) {
		future, err := NewIntroducedBaselineObject("negative-source", "op-create-a-2", definition)
		require.NoError(t, err)
		original, err := NewIntroducedBaselineObject("negative-source", "op-create-a-1", definition)
		require.NoError(t, err)
		require.Equal(t, ObjectID("bd2e3228b32462b66ae8fe0b358eac747edac6da74f891c50797630be66ea180"), future.ID())
		require.Equal(t, ObjectID("bb0fef82fcc7c2743b5f5958d7286bd2d483505db2ccb72790699294bddb9e3f"), original.ID())
		require.Equal(t, OperationID("op-create-a-2"), future.IntroducedBy())
		require.Equal(t, OperationID("op-create-a-1"), original.IntroducedBy())
		require.Len(t, string(future.ID()), 64)
		require.Len(t, string(original.ID()), 64)
		require.NotEqual(t, future.ID(), original.ID())
		baseline := catalogForObjects(t, profile, "negative-source", []CatalogObject{}...)
		after := catalogForObjects(t, profile, "negative-source", newObject(t, future.ID(), definition))
		create := operationFor(t, "op-create-a-1", OperationCreateTable, future.ID(), nil, nil, nil, after, "CREATE TABLE a (id INTEGER)")
		err = newPlanErr(t, baseline, []ResolvedCatalogStep{newStep(t, create, after)}, nil,
			[]Operation{create}, []BaselineObject{future}, nil)
		require.ErrorIs(t, err, ErrInvalidIdentity)
		require.ErrorContains(t, err, "does not name its create_table operation")
	})
}

func operationForOccurrence(t *testing.T, id OperationID, kind OperationKind, object ObjectID, depends []OperationID, after Catalog, sql string) (Operation, error) {
	t.Helper()
	digest, err := CatalogDigest(after)
	if err != nil {
		return Operation{}, err
	}
	return NewOperation(id, kind, depends, []ObjectID{object}, nil, nil,
		digest, []stmt.Statement{stmt.New(sqltext.Text(sql))}, TransactionEngineDefault, false, []stmt.Statement{})
}

func requireActiveCatalog(t *testing.T, catalog Catalog, want map[string]ObjectID, inactive map[ObjectID]string) {
	t.Helper()
	actual := make(map[string]ObjectID, len(catalog.physical.Objects))
	actualIDs := make(map[ObjectID]struct{}, len(catalog.physical.Objects))
	for _, object := range catalog.physical.Objects {
		key := string(object.Kind) + "\x00" + object.Schema + "\x00" + object.Name
		actual[key] = ObjectID(object.ID)
		actualIDs[ObjectID(object.ID)] = struct{}{}
	}
	expected := make(map[string]ObjectID, len(want))
	expectedIDs := make(map[ObjectID]struct{}, len(want))
	for qualified, wantID := range want {
		parts := strings.SplitN(qualified, ".", 2)
		require.Len(t, parts, 2)
		key := string(schema.ObjectTable) + "\x00" + parts[0] + "\x00" + parts[1]
		expected[key] = wantID
		expectedIDs[wantID] = struct{}{}
	}
	require.Equal(t, expected, actual)
	require.Equal(t, expectedIDs, actualIDs)
	for qualified, wantID := range want {
		parts := strings.SplitN(qualified, ".", 2)
		require.Len(t, parts, 2)
		gotID, ok := catalog.ObjectID(schema.ObjectTable, parts[0], parts[1])
		require.Truef(t, ok, "active object %s is missing", qualified)
		require.Equal(t, wantID, gotID, qualified)
	}
	for id, qualified := range inactive {
		parts := strings.SplitN(qualified, ".", 2)
		require.Len(t, parts, 2)
		_, present := actualIDs[id]
		require.Falsef(t, present, "inactive object %s is retained", id)
		gotID, ok := catalog.ObjectID(schema.ObjectTable, parts[0], parts[1])
		if ok {
			require.NotEqualf(t, id, gotID, "inactive object %s is still addressable as %s", id, qualified)
		}
	}
}
