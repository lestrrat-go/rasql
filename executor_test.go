package rasql_test

import (
	"context"
	"database/sql"
	"errors"
	"iter"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/row"
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

// nilTransactionBeginner reports success while handing back no transaction.
// *sql.DB never behaves this way, but Beginner is exported so callers can pass
// a test double, a logging wrapper, or a debug wrapper, and one of those can.
type nilTransactionBeginner struct{}

func (nilTransactionBeginner) BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error) {
	return nil, nil
}

func TestBeginRejectsNilTransactionFromBeginner(t *testing.T) {
	// Begin used to hand the nil transaction to New, which rejected it, and
	// then call Rollback on it while cleaning up, which panicked.
	tx, err := rasql.Begin(t.Context(), nilTransactionBeginner{}, dialect.SQLite(), nil)
	require.ErrorContains(t, err, "rasql: beginner returned a nil transaction")
	require.Equal(t, rasql.Tx{}, tx)
}

// requireLazyRowSequence asserts that obtaining a row sequence and abandoning it
// without ranging leaves no cursor open, and that ranging one to completion
// closes the rows it opens.
//
// expect registers exactly one query expectation marked RowsWillBeClosed, and
// obtain returns a fresh sequence for that query. The expectation is registered
// once and consumed by the ranged pass, so the abandoned pass leaves it
// unmatched: that unmatched expectation is the proof that no query ran and
// therefore no rows were opened. An implementation that queries before
// returning the sequence matches the expectation during the abandoned pass and
// leaves its rows open, which ExpectationsWereMet reports as
// "expected query rows to be closed, but it was not".
func requireLazyRowSequence[T any](t *testing.T, mock sqlmock.Sqlmock, expect func(), obtain func() (iter.Seq2[T, error], error)) {
	t.Helper()

	expect()

	abandoned, err := obtain()
	require.NoError(t, err)
	require.NotNil(t, abandoned)
	require.ErrorContains(t, mock.ExpectationsWereMet(), "there is a remaining expectation which was not matched")

	ranged, err := obtain()
	require.NoError(t, err)
	count := 0
	for _, err := range ranged {
		require.NoError(t, err)
		count++
	}
	require.Equal(t, 1, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

// leakTestClient returns a client over a mock whose expectations are checked
// when the test ends.
func leakTestClient(t *testing.T) (rasql.Client, sqlmock.Sqlmock) {
	t.Helper()

	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	return client, mock
}

func TestQueryRunsNothingUntilTheSequenceIsRanged(t *testing.T) {
	client, mock := leakTestClient(t)
	users := deleteUsersTable(t)
	id, err := users.QueryTable().Column("id")
	require.NoError(t, err)
	statement, err := query.NewSelect(users.QueryTable(), query.Project(id))
	require.NoError(t, err)

	requireLazyRowSequence(t, mock,
		func() {
			mock.ExpectQuery(`SELECT "users"."id" FROM "users"`).
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(42))).
				RowsWillBeClosed()
		},
		func() (iter.Seq2[row.Dynamic, error], error) {
			return rasql.Query(t.Context(), client, statement)
		})
}

func TestQueryWriteRunsNothingUntilTheSequenceIsRanged(t *testing.T) {
	client, mock := leakTestClient(t)
	users := deleteUsersTable(t)
	id, err := users.QueryTable().Column("id")
	require.NoError(t, err)
	email, err := users.QueryTable().Column("email")
	require.NoError(t, err)
	insert, err := query.NewInsert(users.QueryTable(), []query.Column{email}, []query.Expression{query.Bind("ada@example.com")})
	require.NoError(t, err)
	statement, err := insert.WithReturning(query.Project(id))
	require.NoError(t, err)

	requireLazyRowSequence(t, mock,
		func() {
			mock.ExpectQuery(`INSERT INTO "users" ("email") VALUES ($1) RETURNING "id"`).
				WithArgs("ada@example.com").
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(42))).
				RowsWillBeClosed()
		},
		func() (iter.Seq2[row.Dynamic, error], error) {
			return rasql.QueryWrite(t.Context(), client, statement)
		})
}

func TestSelectBuilderQueryRunsNothingUntilTheSequenceIsRanged(t *testing.T) {
	client, mock := leakTestClient(t)
	users := deleteUsersTable(t)

	requireLazyRowSequence(t, mock,
		func() {
			mock.ExpectQuery(`SELECT "users"."id" FROM "users"`).
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(42))).
				RowsWillBeClosed()
		},
		func() (iter.Seq2[row.Dynamic, error], error) {
			return rasql.SelectQueryFrom(users.QueryTable()).Select("id").Query(t.Context(), client)
		})
}

// TypedSelectBuilder.Query delegates to SelectBuilder.Query, so it inherits
// whichever rule that one follows. This checks the inherited behavior rather
// than assuming it.
func TestTypedSelectBuilderQueryRunsNothingUntilTheSequenceIsRanged(t *testing.T) {
	client, mock := leakTestClient(t)
	users := deleteUsersTable(t)

	requireLazyRowSequence(t, mock,
		func() {
			mock.ExpectQuery(`SELECT "users"."id", "users"."email" FROM "users"`).
				WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(int64(42), "ada@example.com")).
				RowsWillBeClosed()
		},
		func() (iter.Seq2[deleteUser, error], error) {
			return rasql.SelectFrom(users).Query(t.Context(), client)
		})
}
