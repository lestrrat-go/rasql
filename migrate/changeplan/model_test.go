package changeplan_test

import (
	"bytes"
	"testing"

	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

type testProfileSource struct{ value engineprofile.Profile }

func (s testProfileSource) ID() string                        { return s.value.ID }
func (s testProfileSource) Engine() changeplan.EngineID       { return s.value.Engine }
func (s testProfileSource) Version() changeplan.EngineVersion { return s.value.Version }
func (s testProfileSource) Capabilities() changeplan.EngineCapabilities {
	return s.value.Capabilities
}
func (s testProfileSource) Limits() changeplan.EngineLimits { return s.value.Limits }

func testProfile(t *testing.T) changeplan.Profile {
	t.Helper()
	profile, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 35})
	require.NoError(t, err)
	snapshot, err := changeplan.NewProfile(testProfileSource{value: profile})
	require.NoError(t, err)
	return snapshot
}

func testBaseline(t *testing.T, future changeplan.BaselineObject, renames ...changeplan.BaselineRename) changeplan.BaselineIdentity {
	t.Helper()
	profile := testProfile(t)
	profileDigest, err := changeplan.ProfileDigest(profile)
	require.NoError(t, err)
	catalog, err := changeplan.NewCatalogIdentity(engineprofile.SQLite, profileDigest, changeplan.Digest{2}, changeplan.Digest{3})
	require.NoError(t, err)
	starting, err := changeplan.NewBaselineObject("starting", "table", "main", "users")
	require.NoError(t, err)
	objects := []changeplan.BaselineObject{starting}
	if future.ID() != "" {
		objects = append(objects, future)
	}
	baseline, err := changeplan.NewBaselineIdentity(catalog, "source", objects, renames)
	require.NoError(t, err)
	return baseline
}

func TestPlanRoundTripAndArgumentIsolation(t *testing.T) {
	future, err := changeplan.NewIntroducedBaselineObject("source", "create", schema.TableDef{Schema: "main", Name: "created", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	baseline := testBaseline(t, future)
	argument := []byte("value")
	statement := stmt.New(sqltext.Text("INSERT INTO created VALUES (?)"), argument)
	operation, err := changeplan.NewOperation("create", changeplan.OperationCreateTable, nil, []changeplan.ObjectID{future.ID()}, nil, nil, []stmt.Statement{stmt.New(sqltext.Text("CREATE TABLE created (id INTEGER)"))}, changeplan.TransactionRequired, false, []stmt.Statement{})
	require.NoError(t, err)
	backfill, err := changeplan.NewOperation("backfill", changeplan.OperationBackfill, []changeplan.OperationID{"create"}, []changeplan.ObjectID{future.ID()}, nil, nil, []stmt.Statement{statement}, changeplan.TransactionForbidden, false, nil)
	require.NoError(t, err)
	decision, err := changeplan.NewDecision("backfill-decision", changeplan.DecisionSupplyBackfill, future.ID(), "", "", true, "policy source")
	require.NoError(t, err)
	history, err := changeplan.NewHistoryIdentity("main", "schema_migrations")
	require.NoError(t, err)
	plan, err := changeplan.NewPlan(testProfile(t), baseline, history, []changeplan.Decision{decision}, []changeplan.Operation{operation, backfill})
	require.NoError(t, err)
	argument[0] = 'X'
	decodedArgs := plan.Operations()[1].Statements()[0].Args()[0].([]byte)
	require.Equal(t, []byte("value"), decodedArgs)
	encoded, err := changeplan.Encode(plan)
	require.NoError(t, err)
	require.True(t, bytes.HasSuffix(encoded, []byte{'\n'}))
	decoded, err := changeplan.Decode(encoded)
	require.NoError(t, err)
	require.Equal(t, plan.ID(), decoded.ID())
	require.Equal(t, encoded, func() []byte {
		value, encodeErr := changeplan.Encode(decoded)
		require.NoError(t, encodeErr)
		return value
	}())
}

func TestStableTopologicalOrderUsesOriginalOrder(t *testing.T) {
	baseline := testBaseline(t, changeplan.BaselineObject{})
	history, err := changeplan.NewHistoryIdentity("main", "schema_migrations")
	require.NoError(t, err)
	decision, err := changeplan.NewDecision("native", changeplan.DecisionAcceptNativeSQL, "starting", "", "", true, "reviewed")
	require.NoError(t, err)
	first, err := changeplan.NewOperation("first", changeplan.OperationNativeSQL, nil, []changeplan.ObjectID{"starting"}, nil, nil, []stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, changeplan.TransactionForbidden, false, nil)
	require.NoError(t, err)
	second, err := changeplan.NewOperation("second", changeplan.OperationNativeSQL, nil, []changeplan.ObjectID{"starting"}, nil, nil, []stmt.Statement{stmt.New(sqltext.Text("SELECT 2"))}, changeplan.TransactionForbidden, false, nil)
	require.NoError(t, err)
	plan, err := changeplan.NewPlan(testProfile(t), baseline, history, []changeplan.Decision{decision}, []changeplan.Operation{second, first})
	require.NoError(t, err)
	order, err := plan.TopologicalOrder()
	require.NoError(t, err)
	require.Equal(t, []changeplan.OperationID{"second", "first"}, order)
}

func TestFactEvaluationUsesRFC6901Paths(t *testing.T) {
	profile := testProfile(t)
	object, err := changeplan.NewCatalogObject("starting", schema.TableDef{Schema: "main", Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	catalog, err := changeplan.NewCatalog(profile, "source", []changeplan.CatalogObject{object})
	require.NoError(t, err)
	fact, err := changeplan.NewFact("starting", "/name", changeplan.FactOperatorEqual, `"users"`)
	require.NoError(t, err)
	require.Equal(t, changeplan.ObjectID("starting"), fact.Object())
	require.Equal(t, `/name`, fact.Path())
	require.NoError(t, changeplan.EvaluateFact(catalog, fact))
}
