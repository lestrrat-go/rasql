package migrate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestPrepareChangePlanBeforeDatabase(t *testing.T) {
	database, calls := openRecordingDatabase(t)
	t.Cleanup(func() { _ = database.Close() })
	var err error
	runner, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	plan := runnerPlan(t, "rasql_schema_migrations", engineprofile.SQLite, changeplan.TransactionForbidden)
	prepared, err := prepareChangePlan(runner, plan)
	require.NoError(t, err)
	require.Zero(t, *calls)
	require.Len(t, prepared.operations, 1)
	require.Equal(t, plan.ID(), prepared.id)
	require.Equal(t, plan.History().Table()+"_plan_progress", prepared.progressName.bareName)
	require.Equal(t, planModeForbidden, prepared.operations[0].mode)
	before, err := prepared.expectedPrefixDigest(0)
	require.NoError(t, err)
	require.Equal(t, plan.Baseline().Catalog().CatalogDigest(), before)
	after, err := prepared.expectedPrefixDigest(1)
	require.NoError(t, err)
	require.Equal(t, prepared.operations[0].afterDigest, after)
	_, err = prepared.expectedPrefixDigest(2)
	require.Error(t, err)
}

func TestPreparedChangePlanSchedule(t *testing.T) {
	database, calls := openRecordingDatabase(t)
	t.Cleanup(func() { _ = database.Close() })
	var err error
	runner, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	emptyPlan := runnerPlanWithOperations(t, "rasql_schema_migrations", engineprofile.SQLite, nil)
	empty, err := prepareChangePlan(runner, emptyPlan)
	require.NoError(t, err)
	require.Empty(t, empty.operations)
	require.False(t, empty.hasForbidden)
	baselineDigest := emptyPlan.Baseline().Catalog().CatalogDigest()
	prefix, err := empty.expectedPrefixDigest(0)
	require.NoError(t, err)
	require.Equal(t, baselineDigest, prefix)
	_, err = empty.expectedPrefixDigest(-1)
	require.Error(t, err)
	_, err = empty.expectedPrefixDigest(1)
	require.Error(t, err)

	first := scheduleOperationWithArgs(t, "first", nil, changeplan.TransactionRequired, changeplan.Digest{11}, []any{[]byte("payload"), time.Unix(123, 456)})
	second := scheduleOperation(t, "second", []changeplan.OperationID{"first"}, changeplan.TransactionEngineDefault, changeplan.Digest{12})
	third := scheduleOperation(t, "third", []changeplan.OperationID{"second"}, changeplan.TransactionForbidden, changeplan.Digest{13})
	mixedPlan := runnerPlanWithOperations(t, "rasql_schema_migrations", engineprofile.SQLite, []changeplan.Operation{third, second, first})
	prepared, err := prepareChangePlan(runner, mixedPlan)
	require.NoError(t, err)
	require.Len(t, prepared.operations, 3)
	require.True(t, prepared.hasForbidden)
	require.Equal(t, []changeplan.OperationID{"first", "second", "third"}, []changeplan.OperationID{
		prepared.operations[0].operation.ID(), prepared.operations[1].operation.ID(), prepared.operations[2].operation.ID(),
	})
	require.Equal(t, []resolvedChangePlanMode{planModeRequired, planModeRequired, planModeForbidden}, []resolvedChangePlanMode{
		prepared.operations[0].mode, prepared.operations[1].mode, prepared.operations[2].mode,
	})
	expectedIDs := []changeplan.OperationID{"first", "second", "third"}
	expectedSQL := []string{"ALTER TABLE users ADD COLUMN value TEXT", "ALTER TABLE users ADD COLUMN value TEXT", "ALTER TABLE users ADD COLUMN value TEXT"}
	expectedBefore := []changeplan.Digest{mixedPlan.Baseline().Catalog().CatalogDigest(), {11}, {12}}
	expectedAfter := []changeplan.Digest{{11}, {12}, {13}}
	expectedArguments := [][]any{{[]byte("payload"), time.Unix(123, 456)}, {}, {}}
	for pass := 0; pass < 2; pass++ {
		for index, operation := range prepared.operations {
			require.Equal(t, index, operation.index)
			require.Equal(t, expectedIDs[index], operation.operation.ID())
			require.Equal(t, expectedSQL[index], operation.operation.Statements()[0].SQL())
			require.Equal(t, expectedArguments[index], operation.operation.Statements()[0].Args())
			require.Equal(t, expectedBefore[index], operation.beforeDigest)
			require.Equal(t, expectedAfter[index], operation.afterDigest)
			before, err := prepared.expectedPrefixDigest(index)
			require.NoError(t, err)
			require.Equal(t, operation.beforeDigest, before)
			after, err := prepared.expectedPrefixDigest(index + 1)
			require.NoError(t, err)
			require.Equal(t, operation.afterDigest, after)
		}
	}
	firstArgs := prepared.operations[0].operation.Statements()[0].Args()
	require.Equal(t, []byte("payload"), firstArgs[0])
	require.Equal(t, time.Unix(123, 456), firstArgs[1])
	for _, index := range []int{-1, 4} {
		_, err := prepared.expectedPrefixDigest(index)
		require.Error(t, err)
	}
	operations, err := mixedPlan.TopologicalOperations()
	require.NoError(t, err)
	operations[0] = third
	require.Equal(t, changeplan.OperationID("first"), prepared.operations[0].operation.ID())
	statements := prepared.operations[0].operation.Statements()
	args := statements[0].Args()
	require.Len(t, args, 2)
	require.Equal(t, []byte("payload"), args[0])
	require.Equal(t, time.Unix(123, 456), args[1])
	args[0].([]byte)[0] = 'X'
	args[1] = time.Unix(0, 0)
	require.Equal(t, []byte("payload"), prepared.operations[0].operation.Statements()[0].Args()[0])
	require.Equal(t, time.Unix(123, 456), prepared.operations[0].operation.Statements()[0].Args()[1])
	profile := mixedPlan.Profile()
	capabilities := profile.Capabilities()
	capabilities.TransactionalDDL = false
	require.Equal(t, engineprofile.SQLite, prepared.profile.Engine)
	require.Equal(t, profile.ID(), prepared.profile.ID)
	require.True(t, prepared.profile.Capabilities.TransactionalDDL)
	baseline := mixedPlan.Baseline()
	baselineObjects := baseline.Objects()
	baselineObjects[0] = changeplan.BaselineObject{}
	require.Equal(t, "runner-test", prepared.baseline.SourceIdentity())
	history := mixedPlan.History()
	require.Equal(t, "rasql_schema_migrations", history.Table())
	require.Equal(t, "rasql_schema_migrations", prepared.history.Table())
	require.Zero(t, *calls)
}

func TestPrepareChangePlanUsesNoDatabaseConnection(t *testing.T) {
	database, calls := openRecordingDatabase(t)
	t.Cleanup(func() { _ = database.Close() })
	runner, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	_, err = prepareChangePlan(runner, runnerPlanWithOperations(t, "rasql_schema_migrations", engineprofile.SQLite, nil))
	require.NoError(t, err)
	// Unreachable transaction_unknown, missing-dependency, cycle, invalid-profile, and digest rows stay in
	// changeplan.TestValidationContractScalarGrammar, TestConstructorValidationCorpus, and
	// TestResultDigestConstructorAndPlanIdentity; this zero Plan still proves the real Encode guard here.
	_, err = prepareChangePlan(runner, changeplan.Plan{})
	require.Error(t, err)
	require.Zero(t, *calls)
}

func TestPrepareChangePlanRejectsBeforeConnection(t *testing.T) {
	database, calls := openRecordingDatabase(t)
	t.Cleanup(func() { _ = database.Close() })
	var err error
	runner, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	cases := map[string]struct {
		runner Runner
		plan   changeplan.Plan
	}{
		"invalid runner":                     {runner: Runner{}, plan: runnerPlan(t, "rasql_schema_migrations", engineprofile.SQLite, changeplan.TransactionForbidden)},
		"history mismatch":                   {runner: runner, plan: runnerPlan(t, "other_history", engineprofile.SQLite, changeplan.TransactionForbidden)},
		"dialect mismatch":                   {runner: runnerFor(t, database, dialect.PostgreSQL()), plan: runnerPlan(t, "rasql_schema_migrations", engineprofile.SQLite, changeplan.TransactionForbidden)},
		"required without transactional DDL": {runner: runnerFor(t, database, dialect.MySQL()), plan: runnerPlan(t, "rasql_schema_migrations", engineprofile.MySQL, changeplan.TransactionRequired)},
	}
	for name, item := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := prepareChangePlan(item.runner, item.plan)
			require.Error(t, err)
		})
	}
	_, err = prepareChangePlan(runner, changeplan.Plan{})
	require.Error(t, err)
	require.Zero(t, *calls)
}

func TestPrepareChangePlanRejectsOversizedDerivedNames(t *testing.T) {
	database, calls := openRecordingDatabase(t)
	t.Cleanup(func() { _ = database.Close() })
	var err error
	postgresHistory := strings.Repeat("h", 54)
	postgresRunner, err := NewWithHistoryTable(database, dialect.PostgreSQL(), postgresHistory)
	require.NoError(t, err)
	postgresPlan := runnerPlan(t, postgresHistory, engineprofile.PostgreSQL, changeplan.TransactionRequired)
	_, err = prepareChangePlan(postgresRunner, postgresPlan)
	require.Error(t, err)
	mysqlHistory := strings.Repeat("h", 55)
	mysqlRunner, err := NewWithHistoryTable(database, dialect.MySQL(), mysqlHistory)
	require.NoError(t, err)
	mysqlPlan := runnerPlan(t, mysqlHistory, engineprofile.MySQL, changeplan.TransactionForbidden)
	_, err = prepareChangePlan(mysqlRunner, mysqlPlan)
	require.Error(t, err)
	require.Zero(t, *calls)
}

func TestPrepareChangePlanIdentifierBoundaries(t *testing.T) {
	for _, item := range []struct {
		name, history string
		dialect       dialect.Dialect
		engine        engineprofile.EngineID
		valid         bool
	}{
		{"postgresql maximum", strings.Repeat("h", 49), dialect.PostgreSQL(), engineprofile.PostgreSQL, true},
		{"postgresql over maximum", strings.Repeat("h", 50), dialect.PostgreSQL(), engineprofile.PostgreSQL, false},
		{"mysql maximum", strings.Repeat("h", 50), dialect.MySQL(), engineprofile.MySQL, true},
		{"mysql over maximum", strings.Repeat("h", 51), dialect.MySQL(), engineprofile.MySQL, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			database, calls := openRecordingDatabase(t)
			t.Cleanup(func() { _ = database.Close() })
			var err error
			runner, err := NewWithHistoryTable(database, item.dialect, item.history)
			require.NoError(t, err)
			_, err = prepareChangePlan(runner, runnerPlan(t, item.history, item.engine, changeplan.TransactionEngineDefault))
			if item.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Zero(t, *calls)
		})
	}
}

func runnerFor(t *testing.T, database *sql.DB, d dialect.Dialect) Runner {
	t.Helper()
	runner, err := New(database, d)
	require.NoError(t, err)
	return runner
}

func runnerPlan(t *testing.T, history string, engine engineprofile.EngineID, mode changeplan.TransactionMode) changeplan.Plan {
	t.Helper()
	return runnerPlanWithMode(t, history, engine, mode)
}

func runnerPlanWithMode(t *testing.T, history string, engine engineprofile.EngineID, mode changeplan.TransactionMode) changeplan.Plan {
	t.Helper()
	profileID := "sqlite-3.35"
	version := engineprofile.Version{Known: true, Major: 3, Minor: 35}
	switch engine {
	case engineprofile.PostgreSQL:
		profileID, version = "postgresql-16", engineprofile.Version{Known: true, Major: 16}
	case engineprofile.MySQL:
		profileID, version = "mysql-8.4", engineprofile.Version{Known: true, Major: 8, Minor: 4}
	}
	profile, err := engineprofile.Builtin(profileID, version)
	require.NoError(t, err)
	catalogProfile, err := changeplan.NewProfile(runnerProfileSource{value: profile})
	require.NoError(t, err)
	object, err := changeplan.NewCatalogObject("starting", schema.TableDef{Schema: "main", Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	catalog, err := changeplan.NewCatalog(runnerProfileSource{value: profile}, "runner-test", []changeplan.CatalogObject{object})
	require.NoError(t, err)
	profileDigest, err := changeplan.ProfileDigest(catalogProfile)
	require.NoError(t, err)
	catalogDigest, err := changeplan.CatalogDigest(catalog)
	require.NoError(t, err)
	identity, err := changeplan.NewCatalogIdentity(engine, profileDigest, catalogDigest, changeplan.Digest{3})
	require.NoError(t, err)
	baselineObject, err := changeplan.NewBaselineObject("starting", "table", "main", "users")
	require.NoError(t, err)
	baseline, err := changeplan.NewBaselineIdentity(identity, "runner-test", []changeplan.BaselineObject{baselineObject}, []changeplan.BaselineRename{})
	require.NoError(t, err)
	historyIdentity, err := changeplan.NewHistoryIdentity("main", history)
	require.NoError(t, err)
	operation, err := changeplan.NewOperation("op-1", changeplan.OperationNativeSQL, nil, []changeplan.ObjectID{"starting"}, nil, nil, changeplan.Digest{4}, []stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, mode, false, nil)
	require.NoError(t, err)
	decision, err := changeplan.NewDecision("decision-1", changeplan.DecisionAcceptNativeSQL, "starting", "", "", true, "runner test")
	require.NoError(t, err)
	plan, err := changeplan.NewPlan(catalogProfile, baseline, historyIdentity, []changeplan.Decision{decision}, []changeplan.Operation{operation})
	require.NoError(t, err)
	return plan
}

func runnerPlanWithOperations(t *testing.T, history string, engine engineprofile.EngineID, operations []changeplan.Operation) changeplan.Plan {
	t.Helper()
	profileID := "sqlite-3.35"
	version := engineprofile.Version{Known: true, Major: 3, Minor: 35}
	switch engine {
	case engineprofile.PostgreSQL:
		profileID, version = "postgresql-16", engineprofile.Version{Known: true, Major: 16}
	case engineprofile.MySQL:
		profileID, version = "mysql-8.4", engineprofile.Version{Known: true, Major: 8, Minor: 4}
	}
	profile, err := engineprofile.Builtin(profileID, version)
	require.NoError(t, err)
	catalogProfile, err := changeplan.NewProfile(runnerProfileSource{value: profile})
	require.NoError(t, err)
	object, err := changeplan.NewCatalogObject("starting", schema.TableDef{Schema: "main", Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	catalog, err := changeplan.NewCatalog(runnerProfileSource{value: profile}, "runner-test", []changeplan.CatalogObject{object})
	require.NoError(t, err)
	profileDigest, err := changeplan.ProfileDigest(catalogProfile)
	require.NoError(t, err)
	catalogDigest, err := changeplan.CatalogDigest(catalog)
	require.NoError(t, err)
	identity, err := changeplan.NewCatalogIdentity(engine, profileDigest, catalogDigest, changeplan.Digest{3})
	require.NoError(t, err)
	baselineObject, err := changeplan.NewBaselineObject("starting", "table", "main", "users")
	require.NoError(t, err)
	baseline, err := changeplan.NewBaselineIdentity(identity, "runner-test", []changeplan.BaselineObject{baselineObject}, []changeplan.BaselineRename{})
	require.NoError(t, err)
	historyIdentity, err := changeplan.NewHistoryIdentity("main", history)
	require.NoError(t, err)
	decisions := make([]changeplan.Decision, 0)
	for _, operation := range operations {
		if operation.Kind() != changeplan.OperationBackfill {
			continue
		}
		decision, decisionErr := changeplan.NewDecision(changeplan.DecisionID("decision-"+string(operation.ID())), changeplan.DecisionSupplyBackfill, operation.Objects()[0], "", "", true, "runner test")
		require.NoError(t, decisionErr)
		decisions = append(decisions, decision)
	}
	plan, err := changeplan.NewPlan(catalogProfile, baseline, historyIdentity, decisions, operations)
	require.NoError(t, err)
	return plan
}

func scheduleOperation(t *testing.T, id string, dependsOn []changeplan.OperationID, mode changeplan.TransactionMode, digest changeplan.Digest) changeplan.Operation {
	return scheduleOperationWithArgs(t, id, dependsOn, mode, digest, nil)
}

func scheduleOperationWithArgs(t *testing.T, id string, dependsOn []changeplan.OperationID, mode changeplan.TransactionMode, digest changeplan.Digest, args []any) changeplan.Operation {
	t.Helper()
	kind := changeplan.OperationAddColumn
	if len(args) > 0 {
		kind = changeplan.OperationBackfill
	}
	operation, err := changeplan.NewOperation(changeplan.OperationID(id), kind, dependsOn, []changeplan.ObjectID{"starting"}, nil, nil, digest, []stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE users ADD COLUMN value TEXT"), args...)}, mode, false, nil)
	require.NoError(t, err)
	return operation
}

type recordingConnector struct {
	calls *int
}

func openRecordingDatabase(t *testing.T) (*sql.DB, *int) {
	t.Helper()
	calls := 0
	return sql.OpenDB(recordingConnector{calls: &calls}), &calls
}

func (c recordingConnector) Connect(context.Context) (driver.Conn, error) {
	*c.calls++
	return nil, driver.ErrBadConn
}

func (c recordingConnector) Driver() driver.Driver { return recordingDriver{} }

type recordingDriver struct{}

func (recordingDriver) Open(string) (driver.Conn, error) { return nil, driver.ErrBadConn }

type runnerProfileSource struct{ value engineprofile.Profile }

func (s runnerProfileSource) ID() string                        { return s.value.ID }
func (s runnerProfileSource) Engine() changeplan.EngineID       { return s.value.Engine }
func (s runnerProfileSource) Version() changeplan.EngineVersion { return s.value.Version }
func (s runnerProfileSource) Capabilities() changeplan.EngineCapabilities {
	return s.value.Capabilities
}
func (s runnerProfileSource) Limits() changeplan.EngineLimits { return s.value.Limits }
