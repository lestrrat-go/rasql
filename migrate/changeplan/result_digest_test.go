package changeplan

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

var goldenLockResultDigests = map[OperationID]string{
	"add-title": "b55516702ed51811192506587beb5ad9de919eb6f22037f69add20c16eeeae8f",
	"add-extra": "7c248af01b6d9c328a7b808781a6b8f182c7efe281fba6fa2f9637534bfefa31",
}

func resultDigestOperation(t *testing.T, id OperationID, dependsOn []OperationID, digest Digest) Operation {
	t.Helper()
	operation, err := NewOperation(id, OperationAddColumn, dependsOn, []ObjectID{"starting"}, nil, nil, digest,
		[]stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE users ADD COLUMN value TEXT"))}, TransactionRequired, false, nil)
	require.NoError(t, err)
	return operation
}

func resultDigestPlanFixture(t *testing.T, digest Digest) Plan {
	t.Helper()
	profile := sourceRepairProfile(t)
	profileDigest, err := ProfileDigest(profile)
	require.NoError(t, err)
	catalog, err := NewCatalog(profile, "result-source", []CatalogObject{})
	require.NoError(t, err)
	catalogDigest, err := CatalogDigest(catalog)
	require.NoError(t, err)
	catalogIdentity, err := NewCatalogIdentity(profile.Engine(), profileDigest, catalogDigest, Digest{3})
	require.NoError(t, err)
	object, err := NewBaselineObject("starting", "table", "main", "users")
	require.NoError(t, err)
	baseline, err := NewBaselineIdentity(catalogIdentity, "result-source", []BaselineObject{object}, nil)
	require.NoError(t, err)
	history, err := NewHistoryIdentity("main", "schema_migrations")
	require.NoError(t, err)
	operation := resultDigestOperation(t, "add-value", nil, digest)
	plan, err := NewPlan(profile, baseline, history, nil, []Operation{operation})
	require.NoError(t, err)
	return plan
}

func resultDigestPlanFromCatalog(t *testing.T, catalog Catalog) Plan {
	t.Helper()
	profile := sourceRepairProfile(t)
	profileDigest, err := ProfileDigest(profile)
	require.NoError(t, err)
	catalogDigest, err := CatalogDigest(catalog)
	require.NoError(t, err)
	catalogIdentity, err := NewCatalogIdentity(profile.Engine(), profileDigest, catalogDigest, Digest{3})
	require.NoError(t, err)
	object, err := NewBaselineObject("starting", "table", "main", "users")
	require.NoError(t, err)
	baseline, err := NewBaselineIdentity(catalogIdentity, catalog.SourceIdentity(), []BaselineObject{object}, nil)
	require.NoError(t, err)
	returnPlan, err := NewPlan(profile, baseline, mustHistory(t), nil, []Operation{resultDigestOperation(t, "add-value", nil, Digest{1})})
	require.NoError(t, err)
	return returnPlan
}

func TestResultDigestConstructorAndPlanIdentity(t *testing.T) {
	_, err := NewOperation("missing-digest", OperationAddColumn, nil, []ObjectID{"starting"}, nil, nil, Digest{},
		[]stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE users ADD COLUMN value TEXT"))}, TransactionRequired, false, nil)
	require.ErrorIs(t, err, ErrInvalidOperation)

	first := resultDigestPlanFixture(t, Digest{1})
	second := resultDigestPlanFixture(t, Digest{2})
	require.NotEqual(t, first.ID(), second.ID())
	require.Equal(t, Digest{1}, first.Operations()[0].ResultDigest())

	first.operations[0].resultDigest = Digest{}
	_, err = NewPlan(first.profile, first.baseline, first.history, first.decisions, first.operations)
	require.ErrorIs(t, err, ErrInvalidOperation)
	_, err = Encode(first)
	require.ErrorIs(t, err, ErrInvalidOperation)

	first = resultDigestPlanFixture(t, Digest{1})
	first.operations[0].resultDigest = Digest{9}
	_, err = Encode(first)
	require.ErrorIs(t, err, ErrInvalidPlan)
}

func TestResultDigestOwnership(t *testing.T) {
	plan := resultDigestPlanFixture(t, Digest{1})
	encoded, err := Encode(plan)
	require.NoError(t, err)
	returned := plan.Operations()
	returned[0].resultDigest = Digest{9}
	require.Equal(t, Digest{1}, plan.Operations()[0].ResultDigest())
	topological, err := plan.TopologicalOperations()
	require.NoError(t, err)
	topological[0].resultDigest = Digest{8}
	topological, err = plan.TopologicalOperations()
	require.NoError(t, err)
	require.Equal(t, Digest{1}, topological[0].ResultDigest())
	reencoded, err := Encode(plan)
	require.NoError(t, err)
	require.Equal(t, encoded, reencoded)

	definition := schema.TableDef{Schema: "main", Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}}
	object, err := NewCatalogObject("starting", definition)
	require.NoError(t, err)
	catalog, err := NewCatalog(sourceRepairProfile(t), "ownership-source", []CatalogObject{object})
	require.NoError(t, err)
	digest, err := CatalogDigest(catalog)
	require.NoError(t, err)
	sourcePlan := resultDigestPlanFromCatalog(t, catalog)
	sourcePlanBytes, err := Encode(sourcePlan)
	require.NoError(t, err)
	definition.Columns[0].Name = "changed"
	require.Equal(t, digest, func() Digest {
		value, digestErr := CatalogDigest(catalog)
		require.NoError(t, digestErr)
		return value
	}())
	sourceReencoded, err := Encode(sourcePlan)
	require.NoError(t, err)
	require.Equal(t, sourcePlanBytes, sourceReencoded)
}

func TestResolvedChangesReturnedCatalogStepsAreOwned(t *testing.T) {
	baseline, afterValue, _ := resultDigestCatalogs(t)
	digest, err := CatalogDigest(afterValue)
	require.NoError(t, err)
	operation := resultDigestOperation(t, "first", nil, digest)
	resolved, err := NewResolvedChanges(baseline, []ResolvedCatalogStep{mustResultStep(t, operation.ID(), afterValue)}, nil,
		[]Operation{operation}, nil, nil)
	require.NoError(t, err)
	steps := resolved.CatalogSteps()
	steps[0].after.physical.Objects[0].Name = "changed"
	got, err := CatalogDigest(resolved.CatalogSteps()[0].Catalog())
	require.NoError(t, err)
	require.Equal(t, digest, got)
}

func resultDigestCatalogs(t *testing.T) (Catalog, Catalog, Catalog) {
	t.Helper()
	profile := sourceRepairProfile(t)
	starting, err := NewCatalogObject("starting", schema.TableDef{Schema: "main", Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	withValue, err := NewCatalogObject("starting", schema.TableDef{Schema: "main", Name: "users", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "value", Type: schema.TextType{}},
	}})
	require.NoError(t, err)
	withMore, err := NewCatalogObject("starting", schema.TableDef{Schema: "main", Name: "users", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "value", Type: schema.TextType{}}, {Name: "more", Type: schema.TextType{}},
	}})
	require.NoError(t, err)
	baseline, err := NewCatalog(profile, "result-source", []CatalogObject{starting})
	require.NoError(t, err)
	afterValue, err := NewCatalog(profile, "result-source", []CatalogObject{withValue})
	require.NoError(t, err)
	afterMore, err := NewCatalog(profile, "result-source", []CatalogObject{withMore})
	require.NoError(t, err)
	return baseline, afterValue, afterMore
}

func TestResolvedChangesUsesStableOrderResultDigests(t *testing.T) {
	baseline, afterValue, afterMore := resultDigestCatalogs(t)
	valueDigest, err := CatalogDigest(afterValue)
	require.NoError(t, err)
	moreDigest, err := CatalogDigest(afterMore)
	require.NoError(t, err)
	first := resultDigestOperation(t, "first", nil, valueDigest)
	second := resultDigestOperation(t, "second", []OperationID{"first"}, moreDigest)
	resolved, err := NewResolvedChanges(baseline, []ResolvedCatalogStep{
		mustResultStep(t, first.ID(), afterValue), mustResultStep(t, second.ID(), afterMore),
	}, nil, []Operation{second, first}, nil, nil)
	require.NoError(t, err)
	require.Equal(t, []OperationID{"first", "second"}, []OperationID{resolved.CatalogSteps()[0].Operation(), resolved.CatalogSteps()[1].Operation()})
	operations := resolved.Operations()
	require.Equal(t, moreDigest, operations[0].ResultDigest())
	require.Equal(t, valueDigest, operations[1].ResultDigest())
	operations[0].resultDigest = Digest{9}
	require.Equal(t, moreDigest, resolved.Operations()[0].ResultDigest())
	_, err = NewResolvedChanges(baseline, []ResolvedCatalogStep{
		mustResultStep(t, first.ID(), afterMore), mustResultStep(t, second.ID(), afterMore),
	}, nil, []Operation{second, first}, nil, nil)
	require.ErrorIs(t, err, ErrInvalidPlan)
	swappedFirst := resultDigestOperation(t, "first", nil, moreDigest)
	swapped := resultDigestOperation(t, "second", []OperationID{"first"}, valueDigest)
	_, err = NewResolvedChanges(baseline, []ResolvedCatalogStep{
		mustResultStep(t, swappedFirst.ID(), afterValue), mustResultStep(t, swapped.ID(), afterMore),
	}, nil, []Operation{swapped, swappedFirst}, nil, nil)
	require.ErrorIs(t, err, ErrInvalidPlan)
}

func mustResultStep(t *testing.T, operation OperationID, catalog Catalog) ResolvedCatalogStep {
	t.Helper()
	step, err := NewResolvedCatalogStep(operation, catalog)
	require.NoError(t, err)
	return step
}

func TestResultDigestWireIsRequiredStrictAndIdentityBound(t *testing.T) {
	plan := resultDigestPlanFixture(t, Digest{1})
	encoded, err := Encode(plan)
	require.NoError(t, err)
	var wire planWire
	require.NoError(t, json.Unmarshal(encoded, &wire))

	for _, test := range []struct {
		name        string
		value       any
		invalidWire bool
	}{
		{name: "missing", invalidWire: true},
		{name: "null", value: nil, invalidWire: true},
		{name: "wrong type", value: 7, invalidWire: true},
		{name: "short", value: "abcd", invalidWire: true},
		{name: "long", value: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", invalidWire: true},
		{name: "nonhex", value: "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", invalidWire: true},
		{name: "uppercase", value: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", invalidWire: true},
		{name: "all zero", value: digestHex(Digest{}), invalidWire: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var root map[string]any
			require.NoError(t, json.Unmarshal(encoded, &root))
			operation := root["operations"].([]any)[0].(map[string]any)
			if test.name == "missing" {
				delete(operation, "result_digest")
			} else {
				operation["result_digest"] = test.value
			}
			mutated, marshalErr := json.Marshal(root)
			require.NoError(t, marshalErr)
			_, decodeErr := Decode(mutated)
			require.Error(t, decodeErr)
			if test.invalidWire {
				require.ErrorIs(t, decodeErr, ErrInvalidWire)
			} else {
				require.ErrorIs(t, decodeErr, ErrInvalidOperation)
			}
		})
	}
	for _, spelling := range []string{"catalog_digest", "resultDigest"} {
		t.Run("unknown "+spelling, func(t *testing.T) {
			var root map[string]any
			require.NoError(t, json.Unmarshal(encoded, &root))
			operation := root["operations"].([]any)[0].(map[string]any)
			operation[spelling] = operation["result_digest"]
			mutated, marshalErr := json.Marshal(root)
			require.NoError(t, marshalErr)
			_, decodeErr := Decode(mutated)
			require.ErrorIs(t, decodeErr, ErrInvalidWire)
		})
	}

	wire.Operations[0].ResultDigest = digestHex(Digest{8})
	mutatedPlan, err := planFromWire(wire)
	require.NoError(t, err)
	mutatedID, err := planDigest(mutatedPlan)
	require.NoError(t, err)
	wire.ID = digestHex(mutatedID)
	mutatedBytes, err := marshalNoHTML(wire)
	require.NoError(t, err)
	decoded, err := Decode(append(mutatedBytes, '\n'))
	require.NoError(t, err)
	require.Equal(t, Digest{8}, decoded.Operations()[0].ResultDigest())
	reencoded, err := Encode(decoded)
	require.NoError(t, err)
	wantReencoded := append(append([]byte(nil), mutatedBytes...), '\n')
	require.Equal(t, wantReencoded, reencoded)

	stale := bytes.Replace(encoded, []byte(digestHex(plan.Operations()[0].ResultDigest())), []byte(digestHex(Digest{8})), 1)
	_, err = Decode(stale)
	require.Error(t, err)
}

func TestFromBaselineRevalidatesResolvedResultDigest(t *testing.T) {
	baseline := resultDigestLockBaseline(t)
	tasks, ok := baseline.ObjectID(schema.ObjectTable, "main", "tasks")
	require.True(t, ok)
	afterObject, err := NewCatalogObject(tasks, schema.TableDef{Schema: "main", Name: "tasks", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "payload", Type: schema.BytesType{}}, {Name: "title", Type: schema.TextType{}},
	}})
	require.NoError(t, err)
	after, err := NewCatalogLike(baseline, []CatalogObject{afterObject})
	require.NoError(t, err)
	digest, err := CatalogDigest(after)
	require.NoError(t, err)
	operation, err := NewOperation("add-title", OperationAddColumn, nil, []ObjectID{tasks}, nil, nil, digest,
		[]stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE tasks ADD COLUMN title TEXT"))}, TransactionEngineDefault, false, nil)
	require.NoError(t, err)
	resolved, err := NewResolvedChanges(baseline, []ResolvedCatalogStep{mustResultStep(t, operation.ID(), after)}, nil,
		[]Operation{operation}, nil, nil)
	require.NoError(t, err)
	validPlan, err := FromBaseline(baseline, resultDigestLockProfile(t), mustHistory(t), resolved)
	require.NoError(t, err)
	require.Equal(t, digest, validPlan.Operations()[0].ResultDigest())
	resolved.operations[0].resultDigest = Digest{7}
	_, err = FromBaseline(baseline, resultDigestLockProfile(t), mustHistory(t), resolved)
	require.ErrorIs(t, err, ErrInvalidPlan)
	resolved.operations[0].resultDigest = digest
	changedObject, err := NewCatalogObject(tasks, schema.TableDef{Schema: "main", Name: "tasks", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "payload", Type: schema.BytesType{}},
		{Name: "title", Type: schema.TextType{}}, {Name: "extra", Type: schema.TextType{}},
	}})
	require.NoError(t, err)
	changedAfter, err := NewCatalogLike(baseline, []CatalogObject{changedObject})
	require.NoError(t, err)
	resolved.steps[0].after = changedAfter
	_, err = FromBaseline(baseline, resultDigestLockProfile(t), mustHistory(t), resolved)
	require.ErrorIs(t, err, ErrInvalidPlan)
}

func TestFromBaselineReverseSourceOrderResultDigestsRoundTrip(t *testing.T) {
	baseline := resultDigestLockBaseline(t)
	tasks, ok := baseline.ObjectID(schema.ObjectTable, "main", "tasks")
	require.True(t, ok)
	makeAfter := func(columns ...schema.ColumnDef) Catalog {
		object, objectErr := NewCatalogObject(tasks, schema.TableDef{Schema: "main", Name: "tasks", Columns: columns})
		require.NoError(t, objectErr)
		catalog, catalogErr := NewCatalogLike(baseline, []CatalogObject{object})
		require.NoError(t, catalogErr)
		return catalog
	}
	baseColumns := []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "payload", Type: schema.BytesType{}}}
	afterTitleColumns := append(append([]schema.ColumnDef(nil), baseColumns...), schema.ColumnDef{Name: "title", Type: schema.TextType{}})
	afterTitle := makeAfter(afterTitleColumns...)
	afterExtraColumns := append(append([]schema.ColumnDef(nil), afterTitleColumns...), schema.ColumnDef{Name: "extra", Type: schema.TextType{}})
	afterExtra := makeAfter(afterExtraColumns...)
	digestTitle, err := CatalogDigest(afterTitle)
	require.NoError(t, err)
	digestExtra, err := CatalogDigest(afterExtra)
	require.NoError(t, err)
	first, err := NewOperation("add-title", OperationAddColumn, nil, []ObjectID{tasks}, nil, nil, digestTitle,
		[]stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE tasks ADD COLUMN title TEXT"))}, TransactionEngineDefault, false, nil)
	require.NoError(t, err)
	second, err := NewOperation("add-extra", OperationAddColumn, []OperationID{"add-title"}, []ObjectID{tasks}, nil, nil, digestExtra,
		[]stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE tasks ADD COLUMN extra TEXT"))}, TransactionRequired, false, nil)
	require.NoError(t, err)
	resolved, err := NewResolvedChanges(baseline, []ResolvedCatalogStep{
		mustResultStep(t, first.ID(), afterTitle), mustResultStep(t, second.ID(), afterExtra),
	}, nil, []Operation{second, first}, nil, nil)
	require.NoError(t, err)
	for _, operation := range resolved.Operations() {
		require.Equal(t, goldenLockResultDigests[operation.ID()], operation.ResultDigest().String())
	}
	plan, err := FromBaseline(baseline, resultDigestLockProfile(t), mustHistory(t), resolved)
	require.NoError(t, err)
	byID := make(map[OperationID]Digest)
	for _, operation := range plan.Operations() {
		byID[operation.ID()] = operation.ResultDigest()
	}
	require.Equal(t, goldenLockResultDigests[first.ID()], byID[first.ID()].String())
	require.Equal(t, goldenLockResultDigests[second.ID()], byID[second.ID()].String())
	encoded, err := Encode(plan)
	require.NoError(t, err)
	decoded, err := Decode(encoded)
	require.NoError(t, err)
	reencoded, err := Encode(decoded)
	require.NoError(t, err)
	require.Equal(t, encoded, reencoded)
	decodedByID := make(map[OperationID]Digest)
	for _, operation := range decoded.Operations() {
		decodedByID[operation.ID()] = operation.ResultDigest()
	}
	require.Equal(t, byID, decodedByID)
	require.Equal(t, goldenLockResultDigests[first.ID()], decodedByID[first.ID()].String())
	require.Equal(t, goldenLockResultDigests[second.ID()], decodedByID[second.ID()].String())
}

func resultDigestLockProfile(t *testing.T) Profile {
	t.Helper()
	base := sourceRepairProfile(t)
	value := engineprofile.Profile{ID: "sqlite-3.35", Engine: engineprofile.SQLite,
		Version: engineprofile.Version{Known: true, Major: 3, Minor: 45}, Capabilities: base.Capabilities(), Limits: base.Limits()}
	profile, err := NewProfile(sourceRepairProfileSource{value: value})
	require.NoError(t, err)
	return profile
}

// resultDigestLockBaseline builds the same "tasks" baseline catalog the
// fixture lock at testdata/external/lock.json has always described, through
// compilerlock.PhysicalFromCatalog and NewCatalogFromPhysical rather than
// the retired CatalogFromLock, so every golden result digest below, computed
// long before this rewrite, still holds.
func resultDigestLockBaseline(t *testing.T) Catalog {
	t.Helper()
	lock, err := os.ReadFile("testdata/external/lock.json")
	require.NoError(t, err)
	file, err := compilerlock.Decode(lock)
	require.NoError(t, err)
	physical := compilerlock.PhysicalFromCatalog(file)
	baseline, err := NewCatalogFromPhysical(physical, file.Source.Identity)
	require.NoError(t, err)
	return baseline
}

func mustHistory(t *testing.T) HistoryIdentity {
	t.Helper()
	history, err := NewHistoryIdentity("main", "schema_migrations")
	require.NoError(t, err)
	return history
}
