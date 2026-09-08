package changeplan

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

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
	definition.Columns[0].Name = "changed"
	require.Equal(t, digest, func() Digest {
		value, digestErr := CatalogDigest(catalog)
		require.NoError(t, digestErr)
		return value
	}())
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
	swapped := resultDigestOperation(t, "second", []OperationID{"first"}, valueDigest)
	_, err = NewResolvedChanges(baseline, []ResolvedCatalogStep{
		mustResultStep(t, first.ID(), afterValue), mustResultStep(t, swapped.ID(), afterMore),
	}, nil, []Operation{swapped, first}, nil, nil)
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

	stale := bytes.Replace(encoded, []byte(digestHex(plan.Operations()[0].ResultDigest())), []byte(digestHex(Digest{8})), 1)
	_, err = Decode(stale)
	require.Error(t, err)
}

func TestFromLockRevalidatesResolvedResultDigest(t *testing.T) {
	lock, err := os.ReadFile("testdata/external/lock.json")
	require.NoError(t, err)
	baseline, err := CatalogFromLock(lock)
	require.NoError(t, err)
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
	validPlan, err := FromLock(lock, lockProfileForResultDigest(t), mustHistory(t), resolved)
	require.NoError(t, err)
	require.Equal(t, digest, validPlan.Operations()[0].ResultDigest())
	resolved.operations[0].resultDigest = Digest{7}
	_, err = FromLock(lock, lockProfileForResultDigest(t), mustHistory(t), resolved)
	require.ErrorIs(t, err, ErrInvalidPlan)
}

func lockProfileForResultDigest(t *testing.T) Profile {
	t.Helper()
	base := sourceRepairProfile(t)
	value := engineprofile.Profile{ID: "sqlite-3.35", Engine: engineprofile.SQLite,
		Version: engineprofile.Version{Known: true, Major: 3, Minor: 45}, Capabilities: base.Capabilities(), Limits: base.Limits()}
	profile, err := NewProfile(sourceRepairProfileSource{value: value})
	require.NoError(t, err)
	return profile
}

func mustHistory(t *testing.T) HistoryIdentity {
	t.Helper()
	history, err := NewHistoryIdentity("main", "schema_migrations")
	require.NoError(t, err)
	return history
}
