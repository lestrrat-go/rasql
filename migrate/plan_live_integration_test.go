//go:build unix

package migrate

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestLiveChangePlanCheckApplyAndReapply(t *testing.T) {
	tests := []struct {
		name      string
		open      func(*testing.T) *sql.DB
		dialect   dialect.Dialect
		engine    engineprofile.EngineID
		profileID string
	}{
		{name: "postgresql", open: dbtest.PostgreSQLDB, dialect: dialect.PostgreSQL(),
			engine: engineprofile.PostgreSQL, profileID: "postgresql-17"},
		{name: "mysql", open: dbtest.MySQLDB, dialect: dialect.MySQL(),
			engine: engineprofile.MySQL, profileID: "mysql-8.4"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := test.open(t)
			history := dbtest.UniqueName(t, "d2h")
			table := dbtest.UniqueName(t, "d2_table")
			plan := liveCreateTablePlan(t, database, test.dialect, test.engine, test.profileID, history, table)
			runner, err := NewWithHistoryTable(database, test.dialect, history)
			require.NoError(t, err)

			check, err := runner.CheckChangePlan(t.Context(), plan)
			require.NoError(t, err)
			require.Equal(t, 0, check.NextOperationIndex())
			require.False(t, check.Complete())
			require.False(t, liveTableExists(t, database, test.dialect.Name(), history))

			result, err := runner.ApplyChangePlan(t.Context(), plan)
			require.NoError(t, err)
			require.Len(t, result.CompletedOperations, 1)
			require.True(t, liveTableExists(t, database, test.dialect.Name(), table))
			require.False(t, liveTableExists(t, database, test.dialect.Name(), history))

			check, err = runner.CheckChangePlan(t.Context(), plan)
			require.NoError(t, err)
			require.Equal(t, 1, check.NextOperationIndex())
			require.True(t, check.Complete())
			result, err = runner.ApplyChangePlan(t.Context(), plan)
			require.NoError(t, err)
			require.Empty(t, result.CompletedOperations)
		})
	}
}

func TestLiveMySQLChangePlanRecoversExecutedDDL(t *testing.T) {
	database := dbtest.MySQLDB(t)
	history := dbtest.UniqueName(t, "d2r")
	table := dbtest.UniqueName(t, "d2_recovery")
	plan := liveCreateTablePlan(t, database, dialect.MySQL(), engineprofile.MySQL, "mysql-8.4", history, table)
	runner, err := NewWithHistoryTable(database, dialect.MySQL(), history)
	require.NoError(t, err)

	checkpointFailure := errors.New("live checkpoint interrupted")
	originalHook := journalWriteHook
	journalWriteHook = func(stage string) error {
		if stage == "checkpoint" {
			return checkpointFailure
		}
		return nil
	}
	t.Cleanup(func() { journalWriteHook = originalHook })
	result, err := runner.ApplyChangePlan(t.Context(), plan)
	require.ErrorIs(t, err, checkpointFailure)
	require.NotNil(t, result.IncompleteOperation)
	require.Equal(t, ChangePlanOperationUnknown, result.IncompleteOperation.Certainty())
	require.True(t, liveTableExists(t, database, "mysql", table))

	_, err = runner.CheckChangePlan(t.Context(), plan)
	var reconciliation *ChangePlanReconciliationError
	require.ErrorAs(t, err, &reconciliation)

	journalWriteHook = originalHook
	result, err = runner.ApplyChangePlan(t.Context(), plan)
	require.NoError(t, err)
	require.Len(t, result.CompletedOperations, 1)
	var progressRows int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+history+"_progress").Scan(&progressRows))
	require.Zero(t, progressRows)
	check, err := runner.CheckChangePlan(t.Context(), plan)
	require.NoError(t, err)
	require.True(t, check.Complete())
}

func TestLivePostgreSQLChangePlanRollsBackPostconditionFailure(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	history := dbtest.UniqueName(t, "d2p")
	table := dbtest.UniqueName(t, "d2_postcondition")
	quotedTable, err := dialect.PostgreSQL().QuoteIdentifier(table)
	require.NoError(t, err)
	plan := liveCreateTablePlanWithSQL(t, database, dialect.PostgreSQL(), engineprofile.PostgreSQL,
		"postgresql-17", history, table,
		"CREATE TABLE "+quotedTable+" (id BIGINT PRIMARY KEY, name TEXT)",
		"CREATE TABLE "+quotedTable+" (id BIGINT PRIMARY KEY)")
	runner, err := NewWithHistoryTable(database, dialect.PostgreSQL(), history)
	require.NoError(t, err)

	result, err := runner.ApplyChangePlan(t.Context(), plan)
	require.Error(t, err)
	require.NotNil(t, result.IncompleteOperation)
	require.Equal(t, ChangePlanStagePostconditions, result.IncompleteOperation.Stage())
	require.Equal(t, ChangePlanOperationRolledBack, result.IncompleteOperation.Certainty())
	require.False(t, liveTableExists(t, database, "postgresql", table))
	require.False(t, liveTableExists(t, database, "postgresql", history+"_plan_progress"))
}

func liveCreateTablePlan(
	t *testing.T,
	database *sql.DB,
	dialectValue dialect.Dialect,
	engine engineprofile.EngineID,
	profileID, historyTable, tableName string,
) changeplan.Plan {
	t.Helper()
	quotedTable, err := dialectValue.QuoteIdentifier(tableName)
	require.NoError(t, err)
	createSQL := "CREATE TABLE " + quotedTable + " (id BIGINT PRIMARY KEY)"
	return liveCreateTablePlanWithSQL(t, database, dialectValue, engine, profileID, historyTable, tableName,
		createSQL, createSQL)
}

func liveCreateTablePlanWithSQL(
	t *testing.T,
	database *sql.DB,
	dialectValue dialect.Dialect,
	engine engineprofile.EngineID,
	profileID, historyTable, tableName, expectedSQL, executionSQL string,
) changeplan.Plan {
	t.Helper()
	profile, err := engineprofile.Discover(t.Context(), database, engine, profileID)
	require.NoError(t, err)
	adapted := planProfileAdapter{profile}
	sourceIdentity := "live-plan-" + tableName
	baseline, err := changeplan.NewCatalog(adapted, sourceIdentity, []changeplan.CatalogObject{})
	require.NoError(t, err)
	quotedTable, err := dialectValue.QuoteIdentifier(tableName)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), expectedSQL)
	require.NoError(t, err)
	afterRead, err := catalogread.Read(t.Context(), database, profile, catalogread.Scope{})
	require.NoError(t, err)
	require.Len(t, afterRead.Tables, 1)
	operationID := changeplan.OperationID("create-table")
	introduced, err := changeplan.NewIntroducedBaselineObject(sourceIdentity, operationID, afterRead.Tables[0])
	require.NoError(t, err)
	afterObject, err := changeplan.NewCatalogObject(introduced.ID(), afterRead.Tables[0])
	require.NoError(t, err)
	after, err := changeplan.NewCatalog(adapted, sourceIdentity, []changeplan.CatalogObject{afterObject})
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "DROP TABLE "+quotedTable)
	require.NoError(t, err)

	profileDigest, err := changeplan.ProfileDigest(adapted)
	require.NoError(t, err)
	baselineDigest, err := changeplan.CatalogDigest(baseline)
	require.NoError(t, err)
	afterDigest, err := changeplan.CatalogDigest(after)
	require.NoError(t, err)
	identity, err := changeplan.NewCatalogIdentity(profile.Engine, profileDigest, baselineDigest, changeplan.Digest{1})
	require.NoError(t, err)
	baselineIdentity, err := changeplan.NewBaselineIdentity(identity, sourceIdentity,
		[]changeplan.BaselineObject{introduced}, nil)
	require.NoError(t, err)
	history, err := changeplan.NewHistoryIdentity("", historyTable)
	require.NoError(t, err)
	operation, err := changeplan.NewOperation(operationID, changeplan.OperationCreateTable, nil,
		[]changeplan.ObjectID{introduced.ID()}, nil, nil, afterDigest,
		[]stmt.Statement{stmt.New(sqltext.Text(executionSQL))}, changeplan.TransactionEngineDefault, false, nil)
	require.NoError(t, err)
	plan, err := changeplan.NewPlan(adapted, baselineIdentity, history, nil, []changeplan.Operation{operation})
	require.NoError(t, err)
	return plan
}

func liveTableExists(t *testing.T, database *sql.DB, dialectName, table string) bool {
	t.Helper()
	var count int
	query := "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1"
	if dialectName == "mysql" {
		query = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?"
	}
	require.NoError(t, database.QueryRowContext(t.Context(), query, table).Scan(&count))
	return count == 1
}
