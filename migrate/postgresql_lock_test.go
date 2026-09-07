package migrate

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/stretchr/testify/require"
)

const postgreSQLLockHistory = `"rasql_schema_migrations"`

func newPostgreSQLLockTestRunner(t *testing.T) (Runner, sqlmock.Sqlmock, func()) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	runner, err := New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	connection, err := database.Conn(t.Context())
	require.NoError(t, err)
	return runner, mock, func() {
		_ = connection.Close()
		_ = database.Close()
	}
}

func TestWithPostgreSQLLockJoinsCanceledOperationAndCleanup(t *testing.T) {
	runner, mock, closeTest := newPostgreSQLLockTestRunner(t)
	defer closeTest()
	database := runner.database
	connection, err := database.Conn(t.Context())
	require.NoError(t, err)
	defer func() { _ = connection.Close() }()
	releaseErr := errors.New("unlock query failed")
	mock.ExpectExec("SELECT pg_advisory_lock(hashtextextended($1, 0))").WithArgs(postgreSQLLockHistory).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT pg_advisory_unlock(hashtextextended($1, 0))").WithArgs(postgreSQLLockHistory).WillReturnError(releaseErr)
	operationContext, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err = runner.withPostgreSQLLock(operationContext, connection, func() ([]Migration, error) {
		cancel()
		return []Migration{{ID: "001"}}, context.Canceled
	})
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, releaseErr)
	require.ErrorIs(t, err, driver.ErrBadConn)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReleasePostgreSQLLockRejectsFalseAndDisposesConnection(t *testing.T) {
	runner, mock, closeTest := newPostgreSQLLockTestRunner(t)
	defer closeTest()
	connection, err := runner.database.Conn(t.Context())
	require.NoError(t, err)
	defer func() { _ = connection.Close() }()
	mock.ExpectQuery("SELECT pg_advisory_unlock(hashtextextended($1, 0))").WithArgs(postgreSQLLockHistory).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(false))
	err = runner.releasePostgreSQLLock(connection)
	require.ErrorContains(t, err, "lock was not held")
	require.ErrorIs(t, err, driver.ErrBadConn)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReleasePostgreSQLLockAcceptsTrue(t *testing.T) {
	runner, mock, closeTest := newPostgreSQLLockTestRunner(t)
	defer closeTest()
	connection, err := runner.database.Conn(t.Context())
	require.NoError(t, err)
	defer func() { _ = connection.Close() }()
	mock.ExpectQuery("SELECT pg_advisory_unlock(hashtextextended($1, 0))").WithArgs(postgreSQLLockHistory).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(true))
	require.NoError(t, runner.releasePostgreSQLLock(connection))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReleasePostgreSQLLockBoundsCleanup(t *testing.T) {
	runner, mock, closeTest := newPostgreSQLLockTestRunner(t)
	defer closeTest()
	connection, err := runner.database.Conn(t.Context())
	require.NoError(t, err)
	defer func() { _ = connection.Close() }()
	mock.ExpectQuery("SELECT pg_advisory_unlock(hashtextextended($1, 0))").WithArgs(postgreSQLLockHistory).
		WillDelayFor(postgreSQLLockReleaseTimeout + time.Second).WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(true))
	started := time.Now()
	err = runner.releasePostgreSQLLock(connection)
	require.Error(t, err)
	require.GreaterOrEqual(t, time.Since(started), postgreSQLLockReleaseTimeout)
	require.Less(t, time.Since(started), postgreSQLLockReleaseTimeout+2*time.Second)
	require.ErrorIs(t, err, driver.ErrBadConn)
	require.NoError(t, mock.ExpectationsWereMet())
}
