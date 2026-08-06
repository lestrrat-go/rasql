package rasql_test

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/render"
	"github.com/stretchr/testify/require"
)

func TestBeginReturnsTxBoundToDialect(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	mock.ExpectBegin()
	mock.ExpectRollback()

	tx, err := rasql.Begin(t.Context(), database, dialect.PostgreSQL(), nil)
	require.NoError(t, err)
	require.Equal(t, "postgresql", tx.Dialect().Name())
	require.NoError(t, tx.Rollback())
}

func TestTxWriteReachesTransactionAndCommits(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO users (id) VALUES (?)").
		WithArgs(42).
		WillReturnResult(sqlmock.NewResult(42, 1))
	mock.ExpectCommit()

	tx, err := rasql.Begin(t.Context(), database, dialect.SQLite(), nil)
	require.NoError(t, err)
	defer tx.Rollback()

	statement, err := render.Precompiled("INSERT INTO users (id) VALUES (?)", 42)
	require.NoError(t, err)
	result, err := tx.ExecRendered(t.Context(), statement)
	require.NoError(t, err)
	rows, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	require.NoError(t, tx.Commit())
}

func TestTxRollbackAfterCommitReportsNoError(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	mock.ExpectBegin()
	mock.ExpectCommit()

	tx, err := rasql.Begin(t.Context(), database, dialect.SQLite(), nil)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	// The mock records no expectation for a rollback: this proves Rollback
	// after a successful Commit reports nil without reaching the driver again.
	require.NoError(t, tx.Rollback())
}

func TestTxRollbackTwiceReportsNoErrorEitherTime(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	mock.ExpectBegin()
	mock.ExpectRollback()

	tx, err := rasql.Begin(t.Context(), database, dialect.SQLite(), nil)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	require.NoError(t, tx.Rollback())
}

func TestBeginPropagatesBeginTxError(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	expected := errors.New("connection refused")
	mock.ExpectBegin().WillReturnError(expected)

	_, err = rasql.Begin(t.Context(), database, dialect.SQLite(), nil)
	require.ErrorContains(t, err, "rasql: begin transaction")
	require.ErrorIs(t, err, expected)
}

func TestBeginRejectsNilDialectWithoutCallingBeginTx(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	// No ExpectBegin(): a call to BeginTx would fail ExpectationsWereMet.

	_, err = rasql.Begin(t.Context(), database, nil, nil)
	require.ErrorContains(t, err, "rasql: dialect must not be nil")
}

func TestZeroTxRejectsCommitAndRollback(t *testing.T) {
	var tx rasql.Tx
	require.ErrorContains(t, tx.Commit(), "rasql: invalid transaction")
	require.ErrorContains(t, tx.Rollback(), "rasql: invalid transaction")
}
