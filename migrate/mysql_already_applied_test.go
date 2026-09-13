package migrate

import (
	"bytes"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	gomysql "github.com/go-sql-driver/mysql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

func alreadyAppliedFixture(t *testing.T) (*bytes.Buffer, Runner, sqlmock.Sqlmock) {
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
	notices := &bytes.Buffer{}
	return notices, runner.WithNotices(notices), mock
}

func TestMySQLNonTransactionalApplyToleratesAlreadyApplied(t *testing.T) {
	notices, runner, mock := alreadyAppliedFixture(t)
	migration := Migration{ID: "001_add_note", Mode: ExecutionModeNonTransactional, Statements: []Statement{
		{Source: "001_add_note.up.sql", SQL: sqltext.Text("ALTER TABLE users ADD COLUMN note VARCHAR(20)")},
	}}
	duplicate := &gomysql.MySQLError{Number: 1060, SQLState: [5]byte{'4', '2', 'S', '2', '1'}, Message: "Duplicate column name 'note'"}
	mock.ExpectQuery("SELECT GET_LOCK(?, ?)").WithArgs(defaultHistoryTable, 30).WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(1))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS `rasql_schema_migrations` (`id` VARCHAR(255) NOT NULL PRIMARY KEY, `checksum` CHAR(64) NOT NULL, `applied_at` TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT `id`, `checksum` FROM `rasql_schema_migrations` ORDER BY `id`").WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}))
	mock.ExpectExec(string(migration.Statements[0].SQL)).WillReturnError(duplicate)
	mock.ExpectExec("INSERT INTO `rasql_schema_migrations` (`id`, `checksum`) VALUES (?, ?)").WithArgs(migration.ID, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT RELEASE_LOCK(?)").WithArgs(defaultHistoryTable).WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))
	completed, err := runner.Apply(t.Context(), AllPending(), migration)
	require.NoError(t, err)
	require.Len(t, completed, 1)
	require.Equal(t, "migrate: warning: migration \"001_add_note\" SQL source \"001_add_note.up.sql\" was already applied: Error 1060 (42S21): Duplicate column name 'note'\n", notices.String())
}

func TestPostgreSQLDoesNotTolerateMySQLAlreadyApplied(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	runner, err := New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	notices := &bytes.Buffer{}
	runner = runner.WithNotices(notices)
	migration := Migration{ID: "001_add_note", Mode: ExecutionModeNonTransactional, Statements: []Statement{
		{Source: "001_add_note.up.sql", SQL: sqltext.Text("ALTER TABLE users ADD COLUMN note VARCHAR(20)")},
	}}
	duplicate := &gomysql.MySQLError{Number: 1060, SQLState: [5]byte{'4', '2', 'S', '2', '1'}, Message: "Duplicate column name 'note'"}
	mock.ExpectExec("SELECT pg_advisory_lock(hashtextextended($1, 0))").WithArgs("\"rasql_schema_migrations\"").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS \"rasql_schema_migrations\" (\"id\" TEXT NOT NULL PRIMARY KEY, \"checksum\" TEXT NOT NULL, \"applied_at\" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP)").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT \"id\", \"checksum\" FROM \"rasql_schema_migrations\" ORDER BY \"id\"").WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}))
	mock.ExpectExec(string(migration.Statements[0].SQL)).WillReturnError(duplicate)
	mock.ExpectQuery("SELECT pg_advisory_unlock(hashtextextended($1, 0))").WithArgs("\"rasql_schema_migrations\"").WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(true))
	_, applyErr := runner.Apply(t.Context(), AllPending(), migration)
	require.ErrorContains(t, applyErr, "Duplicate column name")
	require.Empty(t, notices.String())
	var native *gomysql.MySQLError
	require.True(t, errors.As(applyErr, &native))
}
