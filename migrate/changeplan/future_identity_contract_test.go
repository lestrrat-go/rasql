package changeplan

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

type futureIdentityProfileSource struct{ value engineprofile.Profile }

func (s futureIdentityProfileSource) ID() string                       { return s.value.ID }
func (s futureIdentityProfileSource) Engine() EngineID                 { return s.value.Engine }
func (s futureIdentityProfileSource) Version() EngineVersion           { return s.value.Version }
func (s futureIdentityProfileSource) Capabilities() EngineCapabilities { return s.value.Capabilities }
func (s futureIdentityProfileSource) Limits() EngineLimits             { return s.value.Limits }

type futureIdentityFixture struct {
	profile   Profile
	baseline  BaselineIdentity
	history   HistoryIdentity
	future    BaselineObject
	operation Operation
	plan      Plan
}

func newFutureIdentityFixture(t *testing.T) futureIdentityFixture {
	t.Helper()
	value, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 35})
	require.NoError(t, err)
	profile, err := NewProfile(futureIdentityProfileSource{value: value})
	require.NoError(t, err)
	profileDigest, err := ProfileDigest(profile)
	require.NoError(t, err)
	catalogIdentity, err := NewCatalogIdentity(profile.Engine(), profileDigest, Digest{}, Digest{})
	require.NoError(t, err)
	starting, err := NewBaselineObject("starting", "table", "main", "users")
	require.NoError(t, err)
	future, err := NewIntroducedBaselineObject("future-source", "create", schema.TableDef{
		Schema: "main", Name: "created", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	})
	require.NoError(t, err)
	baseline, err := NewBaselineIdentity(catalogIdentity, "future-source", []BaselineObject{starting, future}, nil)
	require.NoError(t, err)
	history, err := NewHistoryIdentity("main", "schema_migrations")
	require.NoError(t, err)
	operation, err := NewOperation("create", OperationCreateTable, nil, []ObjectID{future.ID()}, nil, nil,
		Digest{1}, []stmt.Statement{stmt.New(sqltext.Text("CREATE TABLE created (id INTEGER)"))}, TransactionRequired, false, nil)
	require.NoError(t, err)
	plan, err := newPlan(profile, baseline, history, nil, []Operation{operation})
	require.NoError(t, err)
	return futureIdentityFixture{profile: profile, baseline: baseline, history: history, future: future, operation: operation, plan: plan}
}

func TestFutureIdentityRejectedAtNewPlan(t *testing.T) {
	fixture := newFutureIdentityFixture(t)
	fixture.baseline.objects[1].id = "corrupt-future-id"
	_, err := newPlan(fixture.profile, fixture.baseline, fixture.history, nil, []Operation{fixture.operation})
	require.ErrorIs(t, err, ErrInvalidIdentity)
}

func TestFutureIdentityRejectedAtEncode(t *testing.T) {
	fixture := newFutureIdentityFixture(t)
	fixture.plan.baseline.objects[1].id = "corrupt-future-id"
	_, err := Encode(fixture.plan)
	require.ErrorIs(t, err, ErrInvalidIdentity)
}

func TestFutureIdentityRejectedAtDecode(t *testing.T) {
	fixture := newFutureIdentityFixture(t)
	encoded, err := Encode(fixture.plan)
	require.NoError(t, err)
	var root map[string]any
	require.NoError(t, json.Unmarshal(encoded, &root))
	baseline := root["baseline"].(map[string]any)
	objects := baseline["objects"].([]any)
	objects[1].(map[string]any)["id"] = "corrupt-future-id"
	mutated, err := json.Marshal(root)
	require.NoError(t, err)
	_, err = Decode(mutated)
	require.ErrorIs(t, err, ErrInvalidIdentity)
}

func TestFutureIdentityRejectedAtNewResolvedChanges(t *testing.T) {
	fixture := newFutureIdentityFixture(t)
	startingObject, err := NewCatalogObject("starting", schema.TableDef{Schema: "main", Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	createdObject, err := NewCatalogObject(fixture.future.ID(), schema.TableDef{Schema: "main", Name: "created", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	baseline, err := NewCatalog(fixture.profile, "future-source", []CatalogObject{startingObject})
	require.NoError(t, err)
	after, err := NewCatalog(fixture.profile, "future-source", []CatalogObject{startingObject, createdObject})
	require.NoError(t, err)
	resultDigest, err := CatalogDigest(after)
	require.NoError(t, err)
	operation, err := NewOperation(fixture.operation.ID(), fixture.operation.Kind(), fixture.operation.DependsOn(), fixture.operation.Objects(), nil, nil,
		resultDigest, fixture.operation.Statements(), fixture.operation.Transaction(), fixture.operation.Reversible(), fixture.operation.ReverseStatements())
	require.NoError(t, err)
	step, err := NewResolvedCatalogStep(operation.ID(), after)
	require.NoError(t, err)
	badFuture := fixture.future
	badFuture.id = "corrupt-future-id"
	_, err = NewResolvedChanges(baseline, []ResolvedCatalogStep{step}, nil, []Operation{operation}, []BaselineObject{badFuture}, nil)
	require.ErrorIs(t, err, ErrInvalidIdentity)
}

func TestFutureIdentityRejectedAtFromLock(t *testing.T) {
	lock, err := os.ReadFile("testdata/external/lock.json")
	require.NoError(t, err)
	fixture := newFutureIdentityFixture(t)
	future, err := NewIntroducedBaselineObject("fixtures/sqlite", "create", schema.TableDef{
		Schema: "main", Name: "created", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	})
	require.NoError(t, err)
	operation, err := NewOperation("create", OperationCreateTable, nil, []ObjectID{future.ID()}, nil, nil,
		Digest{1}, []stmt.Statement{stmt.New(sqltext.Text("CREATE TABLE created (id INTEGER)"))}, TransactionRequired, false, nil)
	require.NoError(t, err)
	baseline, err := CatalogFromLock(lock)
	require.NoError(t, err)
	tasks, ok := baseline.ObjectID(schema.ObjectTable, "main", "tasks")
	require.True(t, ok)
	startingObject, err := NewCatalogObject(tasks, schema.TableDef{Schema: "main", Name: "tasks", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	createdObject, err := NewCatalogObject(future.ID(), schema.TableDef{Schema: "main", Name: "created", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	after, err := NewCatalogLike(baseline, []CatalogObject{startingObject, createdObject})
	require.NoError(t, err)
	resultDigest, err := CatalogDigest(after)
	require.NoError(t, err)
	operation, err = NewOperation(operation.ID(), operation.Kind(), operation.DependsOn(), operation.Objects(), nil, nil,
		resultDigest, operation.Statements(), operation.Transaction(), operation.Reversible(), operation.ReverseStatements())
	require.NoError(t, err)
	step, err := NewResolvedCatalogStep(operation.ID(), after)
	require.NoError(t, err)
	badFuture := future
	badFuture.id = "corrupt-future-id"
	resolved, err := NewResolvedChanges(baseline, []ResolvedCatalogStep{step}, nil, []Operation{operation}, []BaselineObject{future}, nil)
	require.NoError(t, err)
	resolved.futureObjects[0] = badFuture
	lockProfile, err := NewProfile(futureIdentityProfileSource{value: engineprofile.Profile{
		ID: fixture.profile.ID(), Engine: fixture.profile.Engine(),
		Version:      engineprofile.Version{Known: true, Major: 3, Minor: 45, Patch: 0},
		Capabilities: fixture.profile.Capabilities(), Limits: fixture.profile.Limits(),
	}})
	require.NoError(t, err)
	_, err = FromLock(lock, lockProfile, fixture.history, resolved)
	require.ErrorIs(t, err, ErrInvalidIdentity)
}
