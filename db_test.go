package rasql_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestNewRejectsNilDependencies(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	_, err = rasql.New(nil, dialect.PostgreSQL())
	require.ErrorContains(t, err, "rasql: handle must not be nil")

	// No ExpectBegin(): a call to BeginTx would fail ExpectationsWereMet, so
	// this also proves New validates before it touches the handle.
	_, err = rasql.New(database, nil)
	require.ErrorContains(t, err, "rasql: dialect must not be nil")
}

// TestZeroDBRejectsEveryMethodWithoutPanicking checks that a zero DB{} - one
// never returned by New - reports an error from every method that touches its
// handle rather than panicking on a nil one.
func TestZeroDBRejectsEveryMethodWithoutPanicking(t *testing.T) {
	var db rasql.DB

	_, err := db.Begin(t.Context(), nil)
	require.ErrorContains(t, err, "rasql: invalid DB")

	_, err = db.WithHooks()
	require.ErrorContains(t, err, "rasql: invalid DB")

	_, err = db.QueryRendered(t.Context(), renderedSelectStatement(t))
	require.ErrorContains(t, err, "rasql: invalid DB")

	_, err = db.ExecRendered(t.Context(), renderedDeleteStatement(t))
	require.ErrorContains(t, err, "rasql: invalid DB")

	require.Nil(t, db.Dialect())
	require.Nil(t, db.Handle())
	require.ErrorContains(t, db.Commit(), "rasql: this DB is not a transaction")
	require.ErrorContains(t, db.Rollback(), "rasql: this DB is not a transaction")
	require.ErrorContains(t, db.Validate(), "rasql: invalid DB")
}

func TestDialectReturnsConfiguredDialect(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	// dialect.Dialect's built-in implementation carries a func field, which
	// require.Equal (and even ==) cannot compare, so the assertions below check
	// the dialect passed to New by its observable behavior instead of a full
	// value comparison.
	postgres := dialect.PostgreSQL()
	db, err := rasql.New(database, postgres)
	require.NoError(t, err)
	require.Equal(t, "postgresql", db.Dialect().Name())
	require.Equal(t, postgres.Name(), db.Dialect().Name())
	require.Equal(t, postgres.UpsertStyle(), db.Dialect().UpsertStyle())
	require.True(t, db.Dialect().Supports(dialect.CapabilityReturning))
}

// TestHandleReturnsTheHandleItRunsOn covers the accessor that lets an
// application keep only a DB: the handle it was built from comes back out of
// it, and for a transaction that handle is the *sql.Tx itself.
func TestHandleReturnsTheHandleItRunsOn(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	mock.ExpectBegin()
	mock.ExpectRollback()

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	require.Same(t, database, db.Handle())

	tx, err := db.Begin(t.Context(), nil)
	require.NoError(t, err)
	require.IsType(t, &sql.Tx{}, tx.Handle())
	require.NoError(t, tx.Rollback())
}

func TestBeginReturnsDBBoundToDialect(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	mock.ExpectBegin()
	mock.ExpectRollback()

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	tx, err := db.Begin(t.Context(), nil)
	require.NoError(t, err)
	require.Equal(t, "postgresql", tx.Dialect().Name())
	require.NoError(t, tx.Rollback())
}

func TestTransactionWriteReachesTransactionAndCommits(t *testing.T) {
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

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	tx, err := db.Begin(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	s := stmt.New("INSERT INTO users (id) VALUES (?)", 42)
	result, err := tx.ExecRendered(t.Context(), s)
	require.NoError(t, err)
	rows, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	require.NoError(t, tx.Commit())
}

func TestRollbackAfterCommitReportsNoError(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	mock.ExpectBegin()
	mock.ExpectCommit()

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	tx, err := db.Begin(t.Context(), nil)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	// The mock records no expectation for a rollback: this proves Rollback
	// after a successful Commit reports nil without reaching the driver again.
	require.NoError(t, tx.Rollback())
}

func TestRollbackTwiceReportsNoErrorEitherTime(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	mock.ExpectBegin()
	mock.ExpectRollback()

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	tx, err := db.Begin(t.Context(), nil)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	require.NoError(t, tx.Rollback())
}

// TestCommitAndRollbackRejectADBThatIsNotATransaction covers the price of one
// type for both: a DB that never started a transaction reports that from
// Commit and Rollback instead of the compiler refusing the call.
func TestCommitAndRollbackRejectADBThatIsNotATransaction(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	require.ErrorContains(t, db.Commit(), "rasql: this DB is not a transaction")
	require.ErrorContains(t, db.Rollback(), "rasql: this DB is not a transaction")
}

// TestNewFromTransactionAdoptsIt covers the other half of one type for both:
// an application already holding a *sql.Tx hands it to New and gets a DB that
// commits that transaction, with no second type between them.
func TestNewFromTransactionAdoptsIt(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM users").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	transaction, err := database.BeginTx(t.Context(), nil)
	require.NoError(t, err)

	tx, err := rasql.New(transaction, dialect.SQLite())
	require.NoError(t, err)
	_, err = tx.ExecRendered(t.Context(), renderedDeleteStatement(t))
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
}

func TestBeginRejectsADBThatIsAlreadyATransaction(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	mock.ExpectBegin()
	// No second ExpectBegin(): the nested call must be refused before it
	// reaches the driver.
	mock.ExpectRollback()

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	tx, err := db.Begin(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	_, err = tx.Begin(t.Context(), nil)
	require.ErrorContains(t, err, "rasql: this DB is already a transaction")
}

// TestBeginRejectsAHandleThatCannotStartOne covers the handle that reads and
// writes but cannot begin: New accepts it, and only Begin reports the limit.
func TestBeginRejectsAHandleThatCannotStartOne(t *testing.T) {
	db, err := rasql.New(&debugQueryer{}, dialect.SQLite())
	require.NoError(t, err)
	_, err = db.Begin(t.Context(), nil)
	require.ErrorContains(t, err, "cannot start a transaction")
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

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	_, err = db.Begin(t.Context(), nil)
	require.ErrorContains(t, err, "rasql: begin transaction")
	require.ErrorIs(t, err, expected)
}

// nilTransactionHandle reports success while handing back no transaction.
// *sql.DB never behaves this way, but Handle is exported so callers can pass a
// test double, a logging wrapper, or a debug wrapper, and one of those can.
type nilTransactionHandle struct{}

func (nilTransactionHandle) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, nil
}

func (nilTransactionHandle) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, nil
}

func (nilTransactionHandle) BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error) {
	return nil, nil
}

func TestBeginRejectsNilTransactionFromHandle(t *testing.T) {
	// Begin used to hand the nil transaction to New, which rejected it, and
	// then call Rollback on it while cleaning up, which panicked.
	db, err := rasql.New(nilTransactionHandle{}, dialect.SQLite())
	require.NoError(t, err)
	tx, err := db.Begin(t.Context(), nil)
	require.ErrorContains(t, err, "rasql: handle returned a nil transaction")
	require.Equal(t, rasql.DB{}, tx)
}

// TestBeginInheritsAndAppendsHooks checks that a transaction started by Begin
// runs hooks already registered on the DB before hooks passed to Begin itself,
// so a policy hook registered on a DB also applies inside every transaction
// started from it. Before hooks run in that order; After hooks run in reverse.
func TestBeginInheritsAndAppendsHooks(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	var events []string
	dbHook := rasql.HookFunc{
		BeforeFunc: func(context.Context, rasql.Operation) error {
			events = append(events, "db before")
			return nil
		},
		AfterFunc: func(context.Context, rasql.Operation, error) error {
			events = append(events, "db after")
			return nil
		},
	}
	callHook := rasql.HookFunc{
		BeforeFunc: func(context.Context, rasql.Operation) error {
			events = append(events, "call before")
			return nil
		},
		AfterFunc: func(context.Context, rasql.Operation, error) error {
			events = append(events, "call after")
			return nil
		},
	}
	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM users").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()

	db, err := rasql.New(database, dialect.SQLite(), dbHook)
	require.NoError(t, err)
	tx, err := db.Begin(t.Context(), nil, callHook)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecRendered(t.Context(), renderedDeleteStatement(t))
	require.NoError(t, err)
	require.Equal(t, []string{"db before", "call before", "call after", "db after"}, events)
}

// buildOnlyDB returns a DB over a handle that runs nothing, for tests that
// assert on an error raised while a statement is built, before it would reach
// the database. A zero DB{} cannot serve that purpose: every entry point
// rejects one before it builds anything.
func buildOnlyDB(t *testing.T) rasql.DB {
	t.Helper()
	db, err := rasql.New(&debugQueryer{}, dialect.PostgreSQL())
	require.NoError(t, err)
	return db
}

func renderedSelectStatement(t *testing.T) stmt.Statement {
	t.Helper()
	return stmt.New("SELECT id FROM users")
}

func renderedDeleteStatement(t *testing.T) stmt.Statement {
	t.Helper()
	return stmt.New("DELETE FROM users")
}
