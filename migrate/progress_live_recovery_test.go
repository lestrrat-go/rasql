//go:build unix

package migrate

import (
	"context"
	"database/sql"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

func TestMySQLProgressJournalSeamApplyReconnectsAndReleasesLock(t *testing.T) {
	config := dbtest.MySQLConfig(t)
	database := dbtest.MySQLDB(t)
	history := dbtest.UniqueName(t, "p7_apply_seam_history")
	first := dbtest.UniqueName(t, "p7_apply_seam_first")
	second := dbtest.UniqueName(t, "p7_apply_seam_second")
	migration := Migration{ID: "001_apply_seam", Statements: []Statement{
		{Source: "001_first.sql", SQL: sqltext.Text("CREATE TABLE " + first + " (id INT PRIMARY KEY)")},
		{Source: "002_second.sql", SQL: sqltext.Text("CREATE TABLE " + second + " (id INT PRIMARY KEY)")},
	}, Down: []Statement{
		{Source: "002_second.down.sql", SQL: sqltext.Text("DROP TABLE " + second)},
		{Source: "001_first.down.sql", SQL: sqltext.Text("DROP TABLE " + first)},
	}}
	failCheckpoint := 0
	previousHook := journalWriteHook
	journalWriteHook = func(operation string) error {
		if operation == "checkpoint" {
			failCheckpoint++
			if failCheckpoint == 2 {
				return errLiveJournalFailure
			}
		}
		return nil
	}
	t.Cleanup(func() { journalWriteHook = previousHook })
	runner, err := NewWithHistoryTable(database, dialect.MySQL(), history)
	require.NoError(t, err)
	_, err = runner.Apply(t.Context(), AllPending(), migration)
	require.ErrorIs(t, err, errLiveJournalFailure)
	assertLiveProgressAndEffect(t, config, database, history, first, second, 1, 1, true, false)
	assertLiveLockAndPool(t, config, database, history)
	require.NoError(t, database.Close())

	restarted := openLiveDatabase(t, config)
	restartedRunner, err := NewWithHistoryTable(restarted, dialect.MySQL(), history)
	require.NoError(t, err)
	check := &liveSchemaCheck{table: second, wantExists: true}
	require.NoError(t, restartedRunner.Reconcile(t.Context(), check, migration))
	require.Equal(t, ReconcileExecuted, check.decision)
	assertLiveLockAndPool(t, config, restarted, history)
	completed, err := restartedRunner.Apply(t.Context(), AllPending(), migration)
	require.NoError(t, err)
	require.Len(t, completed, 1)
	assertLiveProgressAndEffect(t, config, restarted, history, first, second, 0, 0, true, true)
	assertLiveLockAndPool(t, config, restarted, history)
}

func TestMySQLProgressRevertJournalSeamExecutedReconnects(t *testing.T) {
	config := dbtest.MySQLConfig(t)
	database := dbtest.MySQLDB(t)
	history := dbtest.UniqueName(t, "p7_revert_executed_history")
	first := dbtest.UniqueName(t, "p7_revert_executed_first")
	second := dbtest.UniqueName(t, "p7_revert_executed_second")
	migration := liveTwoTableMigration("001_revert_executed", first, second, false)
	runner, err := NewWithHistoryTable(database, dialect.MySQL(), history)
	require.NoError(t, err)
	require.NoError(t, func() error { _, err := runner.Apply(t.Context(), AllPending(), migration); return err }())
	failCheckpoint := 0
	previousHook := journalWriteHook
	journalWriteHook = func(operation string) error {
		if operation == "checkpoint" {
			failCheckpoint++
			if failCheckpoint == 2 {
				return errLiveJournalFailure
			}
		}
		return nil
	}
	t.Cleanup(func() { journalWriteHook = previousHook })
	_, err = runner.Revert(t.Context(), Steps(1), migration)
	require.ErrorIs(t, err, errLiveJournalFailure)
	assertLiveProgressAndEffect(t, config, database, history, first, second, 1, 1, false, true)
	assertLiveLockAndPool(t, config, database, history)
	require.NoError(t, database.Close())
	restarted := openLiveDatabase(t, config)
	restartedRunner, err := NewWithHistoryTable(restarted, dialect.MySQL(), history)
	require.NoError(t, err)
	check := &liveSchemaCheck{table: first, wantExists: false}
	require.NoError(t, restartedRunner.Reconcile(t.Context(), check, migration))
	require.Equal(t, ReconcileExecuted, check.decision)
	reverted, err := restartedRunner.Revert(t.Context(), Steps(1), migration)
	require.NoError(t, err)
	require.Len(t, reverted, 1)
	assertLiveProgressAndEffect(t, config, restarted, history, first, second, 0, 0, false, false)
	assertLiveLockAndPool(t, config, restarted, history)
}

func TestMySQLProgressRevertNotExecutedReconnectsAndRetries(t *testing.T) {
	config := dbtest.MySQLConfig(t)
	database := dbtest.MySQLDB(t)
	history := dbtest.UniqueName(t, "p7_revert_not_executed_history")
	first := dbtest.UniqueName(t, "p7_revert_not_executed_first")
	second := dbtest.UniqueName(t, "p7_revert_not_executed_second")
	migration := liveTwoTableMigration("001_revert_not_executed", first, second, true)
	runner, err := NewWithHistoryTable(database, dialect.MySQL(), history)
	require.NoError(t, err)
	require.NoError(t, func() error { _, err := runner.Apply(t.Context(), AllPending(), migration); return err }())
	_, err = runner.Revert(t.Context(), Steps(1), migration)
	require.Error(t, err)
	assertLiveProgressAndEffect(t, config, database, history, first, second, 1, 1, false, true)
	assertLiveLockAndPool(t, config, database, history)
	require.NoError(t, database.Close())
	restarted := openLiveDatabase(t, config)
	restartedRunner, err := NewWithHistoryTable(restarted, dialect.MySQL(), history)
	require.NoError(t, err)
	check := &liveSchemaCheck{table: first, wantExists: false}
	require.NoError(t, restartedRunner.Reconcile(t.Context(), check, migration))
	require.Equal(t, ReconcileNotExecuted, check.decision)
	migration.Down[1].SQL = sqltext.Text("DROP TABLE " + first)
	reverted, err := restartedRunner.Revert(t.Context(), Steps(1), migration)
	require.NoError(t, err)
	require.Len(t, reverted, 1)
	assertLiveProgressAndEffect(t, config, restarted, history, first, second, 0, 0, false, false)
	assertLiveLockAndPool(t, config, restarted, history)
}

var errLiveJournalFailure = errorString("live journal failure")

type errorString string

func (e errorString) Error() string { return string(e) }

type liveSchemaCheck struct {
	table      string
	wantExists bool
	decision   ReconcileDecision
}

func (c *liveSchemaCheck) Check(ctx context.Context, connection *sql.Conn, _ IncompleteMigration) (ReconcileDecision, error) {
	var count int
	if err := connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", c.table).Scan(&count); err != nil {
		return "", err
	}
	if (count == 1) == c.wantExists {
		c.decision = ReconcileExecuted
	} else {
		c.decision = ReconcileNotExecuted
	}
	return c.decision, nil
}

func liveTwoTableMigration(id, first, second string, failSecondDown bool) Migration {
	secondDown := "DROP TABLE " + second
	if failSecondDown {
		secondDown = "DROP TABLE " + second + "_absent"
	}
	return Migration{ID: id, Statements: []Statement{
		{Source: "001_first.sql", SQL: sqltext.Text("CREATE TABLE " + first + " (id INT PRIMARY KEY)")},
		{Source: "002_second.sql", SQL: sqltext.Text("CREATE TABLE " + second + " (id INT PRIMARY KEY)")},
	}, Down: []Statement{
		{Source: "002_second.down.sql", SQL: sqltext.Text("DROP TABLE " + second)},
		{Source: "001_first.down.sql", SQL: sqltext.Text(secondDown)},
	}}
}

func openLiveDatabase(t *testing.T, config *mysql.Config) *sql.DB {
	t.Helper()
	connector, err := mysql.NewConnector(config)
	require.NoError(t, err)
	database := sql.OpenDB(connector)
	t.Cleanup(func() { _ = database.Close() })
	require.NoError(t, database.PingContext(t.Context()))
	return database
}

func assertLiveProgressAndEffect(t *testing.T, config *mysql.Config, database *sql.DB, history, first, second string, wantSource, wantNext int, wantFirst, wantSecond bool) {
	t.Helper()
	inspection := openLiveDatabase(t, config)
	var progressCount int
	require.NoError(t, inspection.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+history+"_progress").Scan(&progressCount))
	if progressCount > 0 {
		var source, next int
		require.NoError(t, inspection.QueryRowContext(t.Context(), "SELECT source_index, next_index FROM "+history+"_progress").Scan(&source, &next))
		require.Equal(t, wantSource, source)
		require.Equal(t, wantNext, next)
	}
	for _, table := range []struct {
		name string
		want bool
	}{{first, wantFirst}, {second, wantSecond}} {
		var count int
		require.NoError(t, inspection.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", table.name).Scan(&count))
		require.Equal(t, table.want, count == 1)
	}
	_ = database
}

func assertLiveLockAndPool(t *testing.T, config *mysql.Config, database *sql.DB, history string) {
	t.Helper()
	locker := openLiveDatabase(t, config)
	var acquired int
	require.NoError(t, locker.QueryRowContext(t.Context(), "SELECT GET_LOCK(?, 5)", history).Scan(&acquired))
	require.Equal(t, 1, acquired)
	var released int
	require.NoError(t, locker.QueryRowContext(t.Context(), "SELECT RELEASE_LOCK(?)", history).Scan(&released))
	require.Equal(t, 1, released)
	var value int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT 1").Scan(&value))
	require.Equal(t, 1, value)
}
