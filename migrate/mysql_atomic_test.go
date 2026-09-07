package migrate

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

var (
	errMySQLAtomicBegin   = errors.New("begin failed")
	errMySQLAtomicRecord  = errors.New("history record failed")
	errMySQLAtomicReverse = errors.New("reverse source failed")
	errMySQLAtomicDelete  = errors.New("history delete failed")
	errMySQLAtomicCommit  = errors.New("commit failed")
)

func TestMySQLAtomicApplyRollsBackSourceAndHistory(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	runner, err := New(database, dialect.MySQL())
	require.NoError(t, err)
	migration := Migration{ID: "001_atomic", Statements: []Statement{{Source: "001.sql", SQL: sqltext.Text("UPDATE counters SET value = value + 1")}}}
	mock.ExpectQuery("SELECT GET_LOCK(?, ?)").WithArgs(defaultHistoryTable, 30).WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(1))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS `rasql_schema_migrations` (`id` VARCHAR(255) NOT NULL PRIMARY KEY, `checksum` CHAR(64) NOT NULL, `applied_at` TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT `id`, `checksum` FROM `rasql_schema_migrations` ORDER BY `id`").WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT `id`, `checksum` FROM `rasql_schema_migrations` ORDER BY `id`").WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}))
	mock.ExpectExec(string(migration.Statements[0].SQL)).WillReturnError(errors.New("source failed"))
	mock.ExpectRollback()
	mock.ExpectQuery("SELECT RELEASE_LOCK(?)").WithArgs(defaultHistoryTable).WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))
	completed, err := runner.Apply(t.Context(), AllPending(), migration)
	require.ErrorContains(t, err, "source failed")
	require.Empty(t, completed)
}

func TestMySQLAtomicApplyCommitFailureRollsBack(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	runner, err := New(database, dialect.MySQL())
	require.NoError(t, err)
	migration := Migration{ID: "001_atomic", Statements: []Statement{{Source: "001.sql", SQL: sqltext.Text("UPDATE counters SET value = value + 1")}}}
	mock.ExpectQuery("SELECT GET_LOCK(?, ?)").WithArgs(defaultHistoryTable, 30).WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(1))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS `rasql_schema_migrations` (`id` VARCHAR(255) NOT NULL PRIMARY KEY, `checksum` CHAR(64) NOT NULL, `applied_at` TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT `id`, `checksum` FROM `rasql_schema_migrations` ORDER BY `id`").WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT `id`, `checksum` FROM `rasql_schema_migrations` ORDER BY `id`").WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}))
	mock.ExpectExec(string(migration.Statements[0].SQL)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO `rasql_schema_migrations` (`id`, `checksum`) VALUES (?, ?)").WithArgs(migration.ID, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
	mock.ExpectQuery("SELECT RELEASE_LOCK(?)").WithArgs(defaultHistoryTable).WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))
	completed, err := runner.Apply(t.Context(), AllPending(), migration)
	require.ErrorContains(t, err, "commit failed")
	require.Empty(t, completed)
}

func mysqlAtomicDriverFixture(t *testing.T) (Runner, sqlmock.Sqlmock, Migration) {
	t.Helper()
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	runner, err := New(database, dialect.MySQL())
	require.NoError(t, err)
	migration := Migration{ID: "001_atomic", Statements: []Statement{{Source: "001.sql", SQL: sqltext.Text("UPDATE counters SET value = value + 1")}}, Down: []Statement{{Source: "001.down.sql", SQL: sqltext.Text("UPDATE counters SET value = value - 1")}}}
	return runner, mock, migration
}

func expectMySQLAtomicStart(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("SELECT GET_LOCK(?, ?)").WithArgs(defaultHistoryTable, 30).WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(1))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS `rasql_schema_migrations` (`id` VARCHAR(255) NOT NULL PRIMARY KEY, `checksum` CHAR(64) NOT NULL, `applied_at` TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)").WillReturnResult(sqlmock.NewResult(0, 0))
}

func expectMySQLAtomicApplied(mock sqlmock.Sqlmock, migration Migration) {
	mock.ExpectQuery("SELECT `id`, `checksum` FROM `rasql_schema_migrations` ORDER BY `id`").WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}).AddRow(migration.ID, checksumMode(migration.Mode, migration.Statements)))
}

func expectMySQLAtomicEmpty(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("SELECT `id`, `checksum` FROM `rasql_schema_migrations` ORDER BY `id`").WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}))
}

func expectMySQLAtomicUnlock(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("SELECT RELEASE_LOCK(?)").WithArgs(defaultHistoryTable).WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))
}

func TestMySQLAtomicApplyBeginSuccessAndHistoryRecordFailure(t *testing.T) {
	t.Run("begin failure", func(t *testing.T) {
		runner, mock, migration := mysqlAtomicDriverFixture(t)
		expectMySQLAtomicStart(mock)
		expectMySQLAtomicEmpty(mock)
		mock.ExpectBegin().WillReturnError(errMySQLAtomicBegin)
		expectMySQLAtomicUnlock(mock)
		completed, err := runner.Apply(t.Context(), AllPending(), migration)
		require.ErrorIs(t, err, errMySQLAtomicBegin)
		require.Empty(t, completed)
	})
	t.Run("success", func(t *testing.T) {
		runner, mock, migration := mysqlAtomicDriverFixture(t)
		expectMySQLAtomicStart(mock)
		expectMySQLAtomicEmpty(mock)
		mock.ExpectBegin()
		expectMySQLAtomicEmpty(mock)
		mock.ExpectExec(string(migration.Statements[0].SQL)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("INSERT INTO `rasql_schema_migrations` (`id`, `checksum`) VALUES (?, ?)").WithArgs(migration.ID, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		expectMySQLAtomicUnlock(mock)
		completed, err := runner.Apply(t.Context(), AllPending(), migration)
		require.NoError(t, err)
		require.Len(t, completed, 1)
	})
	t.Run("history record failure", func(t *testing.T) {
		runner, mock, migration := mysqlAtomicDriverFixture(t)
		expectMySQLAtomicStart(mock)
		expectMySQLAtomicEmpty(mock)
		mock.ExpectBegin()
		expectMySQLAtomicEmpty(mock)
		mock.ExpectExec(string(migration.Statements[0].SQL)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("INSERT INTO `rasql_schema_migrations` (`id`, `checksum`) VALUES (?, ?)").WithArgs(migration.ID, sqlmock.AnyArg()).WillReturnError(errMySQLAtomicRecord)
		mock.ExpectRollback()
		expectMySQLAtomicUnlock(mock)
		completed, err := runner.Apply(t.Context(), AllPending(), migration)
		require.ErrorIs(t, err, errMySQLAtomicRecord)
		require.Empty(t, completed)
	})
}

func TestMySQLAtomicRevertDriverFlow(t *testing.T) {
	t.Run("begin failure", func(t *testing.T) {
		runner, mock, migration := mysqlAtomicDriverFixture(t)
		expectMySQLAtomicStart(mock)
		expectMySQLAtomicApplied(mock, migration)
		mock.ExpectBegin().WillReturnError(errMySQLAtomicBegin)
		expectMySQLAtomicUnlock(mock)
		completed, err := runner.Revert(t.Context(), Steps(1), migration)
		require.ErrorIs(t, err, errMySQLAtomicBegin)
		require.Empty(t, completed)
	})
	t.Run("source failure", func(t *testing.T) {
		runner, mock, migration := mysqlAtomicDriverFixture(t)
		expectMySQLAtomicStart(mock)
		expectMySQLAtomicApplied(mock, migration)
		mock.ExpectBegin()
		expectMySQLAtomicApplied(mock, migration)
		mock.ExpectExec(string(migration.Down[0].SQL)).WillReturnError(errMySQLAtomicReverse)
		mock.ExpectRollback()
		expectMySQLAtomicUnlock(mock)
		completed, err := runner.Revert(t.Context(), Steps(1), migration)
		require.ErrorIs(t, err, errMySQLAtomicReverse)
		require.Empty(t, completed)
	})
	t.Run("history delete failure", func(t *testing.T) {
		runner, mock, migration := mysqlAtomicDriverFixture(t)
		expectMySQLAtomicStart(mock)
		expectMySQLAtomicApplied(mock, migration)
		mock.ExpectBegin()
		expectMySQLAtomicApplied(mock, migration)
		mock.ExpectExec(string(migration.Down[0].SQL)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("DELETE FROM `rasql_schema_migrations` WHERE `id` = ?").WithArgs(migration.ID).WillReturnError(errMySQLAtomicDelete)
		mock.ExpectRollback()
		expectMySQLAtomicUnlock(mock)
		completed, err := runner.Revert(t.Context(), Steps(1), migration)
		require.ErrorIs(t, err, errMySQLAtomicDelete)
		require.Empty(t, completed)
	})
	t.Run("commit failure", func(t *testing.T) {
		runner, mock, migration := mysqlAtomicDriverFixture(t)
		expectMySQLAtomicStart(mock)
		expectMySQLAtomicApplied(mock, migration)
		mock.ExpectBegin()
		expectMySQLAtomicApplied(mock, migration)
		mock.ExpectExec(string(migration.Down[0].SQL)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("DELETE FROM `rasql_schema_migrations` WHERE `id` = ?").WithArgs(migration.ID).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit().WillReturnError(errMySQLAtomicCommit)
		expectMySQLAtomicUnlock(mock)
		completed, err := runner.Revert(t.Context(), Steps(1), migration)
		require.ErrorIs(t, err, errMySQLAtomicCommit)
		require.Empty(t, completed)
	})
	t.Run("success", func(t *testing.T) {
		runner, mock, migration := mysqlAtomicDriverFixture(t)
		expectMySQLAtomicStart(mock)
		expectMySQLAtomicApplied(mock, migration)
		mock.ExpectBegin()
		expectMySQLAtomicApplied(mock, migration)
		mock.ExpectExec(string(migration.Down[0].SQL)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("DELETE FROM `rasql_schema_migrations` WHERE `id` = ?").WithArgs(migration.ID).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		expectMySQLAtomicUnlock(mock)
		completed, err := runner.Revert(t.Context(), Steps(1), migration)
		require.NoError(t, err)
		require.Len(t, completed, 1)
	})
}
