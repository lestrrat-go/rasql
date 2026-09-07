package migrate

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/stretchr/testify/require"
)

func TestWithMySQLLockJoinsOperationAndReleaseErrors(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	runner, err := New(database, dialect.MySQL())
	require.NoError(t, err)
	connection, err := database.Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })

	operationErr := errors.New("operation stopped")
	releaseErr := errors.New("release query failed")
	mock.ExpectQuery("SELECT GET_LOCK(?, ?)").WithArgs(defaultHistoryTable, 30).
		WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(1))
	mock.ExpectQuery("SELECT RELEASE_LOCK(?)").WithArgs(defaultHistoryTable).
		WillReturnError(releaseErr)

	_, err = runner.withMySQLLock(t.Context(), connection, func() ([]Migration, error) {
		return []Migration{{ID: "001"}}, operationErr
	})
	require.ErrorIs(t, err, operationErr)
	require.ErrorIs(t, err, releaseErr)
	require.ErrorIs(t, err, driver.ErrBadConn)
	_ = connection.Close()
	require.NoError(t, database.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReleaseMySQLLockRejectsUnexpectedResultAndDiscardsConnection(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	runner, err := New(database, dialect.MySQL())
	require.NoError(t, err)
	connection, err := database.Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })

	mock.ExpectQuery("SELECT RELEASE_LOCK(?)").WithArgs(defaultHistoryTable).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(0))
	err = runner.releaseMySQLLock(connection)
	require.ErrorContains(t, err, "unexpected release result 0")
	require.ErrorIs(t, err, driver.ErrBadConn)
	_ = connection.Close()
	require.NoError(t, database.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReleaseMySQLLockAcceptsSuccessfulResult(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	runner, err := New(database, dialect.MySQL())
	require.NoError(t, err)
	connection, err := database.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })

	mock.ExpectQuery("SELECT RELEASE_LOCK(?)").WithArgs(defaultHistoryTable).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))
	require.NoError(t, runner.releaseMySQLLock(connection))
	_ = connection.Close()
	mock.ExpectClose()
	require.NoError(t, database.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}
