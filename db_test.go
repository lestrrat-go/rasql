package rasql_test

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/render"
	"github.com/stretchr/testify/require"
)

// TestNewDBRejectsNilBeginner covers NewDB's other validation path.
// TestNewDBRejectsNilDialectWithoutCallingBeginTx in executor_test.go covers
// the nil-dialect path.
func TestNewDBRejectsNilBeginner(t *testing.T) {
	_, err := rasql.NewDB(nil, dialect.PostgreSQL())
	require.ErrorContains(t, err, "rasql: beginner must not be nil")
}

// TestZeroDBRejectsEveryMethodWithoutPanicking checks that a zero DB{} - one
// never returned by NewDB - reports an error from every method that touches
// its beginner or client rather than panicking on a nil handle.
func TestZeroDBRejectsEveryMethodWithoutPanicking(t *testing.T) {
	var db rasql.DB

	_, err := db.Begin(t.Context(), nil)
	require.ErrorContains(t, err, "rasql: invalid client")

	_, err = db.WithHooks()
	require.ErrorContains(t, err, "rasql: invalid client")

	require.Nil(t, db.Dialect())

	_, err = db.QueryRendered(t.Context(), renderedSelectStatement(t))
	require.ErrorContains(t, err, "rasql: invalid client")

	_, err = db.ExecRendered(t.Context(), renderedDeleteStatement(t))
	require.ErrorContains(t, err, "rasql: invalid client")

	require.Equal(t, rasql.Client{}, db.Client())
}

func TestDBClientReturnsClientView(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.NewDB(database, dialect.PostgreSQL())
	require.NoError(t, err)
	client := db.Client()
	require.Equal(t, "postgresql", client.Dialect().Name())
}

// TestDBBeginInheritsAndAppendsHooks checks that a transaction started by
// DB.Begin runs hooks already registered on the DB before hooks passed to
// Begin itself, so a policy hook registered on a DB also applies inside
// every transaction started from it. Before hooks run in that order; After
// hooks run in reverse, mirroring Client and Tx.
func TestDBBeginInheritsAndAppendsHooks(t *testing.T) {
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

	db, err := rasql.NewDB(database, dialect.SQLite(), dbHook)
	require.NoError(t, err)
	tx, err := db.Begin(t.Context(), nil, callHook)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecRendered(t.Context(), renderedDeleteStatement(t))
	require.NoError(t, err)
	require.Equal(t, []string{"db before", "call before", "call after", "db after"}, events)
}

func renderedSelectStatement(t *testing.T) render.Statement {
	t.Helper()
	statement, err := render.Precompiled("SELECT id FROM users")
	require.NoError(t, err)
	return statement
}

func renderedDeleteStatement(t *testing.T) render.Statement {
	t.Helper()
	statement, err := render.Precompiled("DELETE FROM users")
	require.NoError(t, err)
	return statement
}
