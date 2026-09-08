package migrate

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestPrepareChangePlanBeforeDatabase(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	runner, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	plan := runnerPlan(t, "rasql_schema_migrations", engineprofile.SQLite, changeplan.TransactionForbidden)
	prepared, err := prepareChangePlan(runner, plan)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
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

func TestPrepareChangePlanRejectsBeforeConnection(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
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
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPrepareChangePlanRejectsOversizedDerivedNames(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
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
	require.NoError(t, mock.ExpectationsWereMet())
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

type runnerProfileSource struct{ value engineprofile.Profile }

func (s runnerProfileSource) ID() string                        { return s.value.ID }
func (s runnerProfileSource) Engine() changeplan.EngineID       { return s.value.Engine }
func (s runnerProfileSource) Version() changeplan.EngineVersion { return s.value.Version }
func (s runnerProfileSource) Capabilities() changeplan.EngineCapabilities {
	return s.value.Capabilities
}
func (s runnerProfileSource) Limits() changeplan.EngineLimits { return s.value.Limits }
