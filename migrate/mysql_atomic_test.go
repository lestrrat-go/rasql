package migrate

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
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
