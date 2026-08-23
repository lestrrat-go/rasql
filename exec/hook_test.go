package exec_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/exec"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestClientHooksRunInOrderAndPreserveStatement(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	events := make([]string, 0, 4)
	first := exec.HookFunc{
		BeforeFunc: func(_ context.Context, operation exec.Operation) error {
			events = append(events, "first before")
			require.Equal(t, exec.ExecOperation, operation.Kind())
			require.Equal(t, "INSERT INTO users (email) VALUES ($1)", operation.SQL())
			args := operation.Args()
			require.Equal(t, []any{"ada@example.com"}, args)
			args[0] = "rewritten by hook"
			return nil
		},
		AfterFunc: func(_ context.Context, operation exec.Operation, err error) error {
			events = append(events, "first after")
			require.NoError(t, err)
			require.Equal(t, []any{"ada@example.com"}, operation.Args())
			return nil
		},
	}
	second := exec.HookFunc{
		BeforeFunc: func(_ context.Context, operation exec.Operation) error {
			events = append(events, "second before")
			require.Equal(t, exec.ExecOperation, operation.Kind())
			return nil
		},
		AfterFunc: func(_ context.Context, operation exec.Operation, err error) error {
			events = append(events, "second after")
			require.NoError(t, err)
			require.Equal(t, "INSERT INTO users (email) VALUES ($1)", operation.SQL())
			return nil
		},
	}
	db, err := exec.New(database, dialect.PostgreSQL(), first, second)
	require.NoError(t, err)
	s := stmt.New("INSERT INTO users (email) VALUES ($1)", "ada@example.com")
	mock.ExpectExec("INSERT INTO users (email) VALUES ($1)").
		WithArgs("ada@example.com").
		WillReturnResult(sqlmock.NewResult(1, 1))

	_, err = db.ExecRendered(t.Context(), s)
	require.NoError(t, err)
	require.Equal(t, []string{"first before", "second before", "second after", "first after"}, events)
}

func TestClientHooksObserveQuery(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	var observed exec.Operation
	db, err := exec.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	db, err = db.WithHooks(exec.HookFunc{
		BeforeFunc: func(_ context.Context, operation exec.Operation) error {
			observed = operation
			return nil
		},
	})
	require.NoError(t, err)
	s := stmt.New("SELECT id FROM users WHERE id = $1", 42)
	mock.ExpectQuery("SELECT id FROM users WHERE id = $1").
		WithArgs(42).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(42))

	rows, err := db.QueryRendered(t.Context(), s)
	require.NoError(t, err)
	require.NoError(t, rows.Close())
	require.Equal(t, exec.QueryOperation, observed.Kind())
	require.Equal(t, "SELECT id FROM users WHERE id = $1", observed.SQL())
	require.Equal(t, []any{42}, observed.Args())
}

func TestHookErrorsPreventOrRejectExecution(t *testing.T) {
	t.Run("before prevents execution", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})
		expected := errors.New("policy denied")
		db, err := exec.New(database, dialect.SQLite(), exec.HookFunc{
			BeforeFunc: func(_ context.Context, operation exec.Operation) error {
				require.Equal(t, exec.ExecOperation, operation.Kind())
				return expected
			},
		})
		require.NoError(t, err)
		s := stmt.New("DELETE FROM users")

		_, err = db.ExecRendered(t.Context(), s)
		require.ErrorIs(t, err, expected)
		require.ErrorContains(t, err, "hook before exec")
	})

	t.Run("after rejects execution result", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})
		expected := errors.New("metrics sink unavailable")
		db, err := exec.New(database, dialect.SQLite(), exec.HookFunc{
			AfterFunc: func(_ context.Context, operation exec.Operation, err error) error {
				require.NoError(t, err)
				require.Equal(t, exec.ExecOperation, operation.Kind())
				return expected
			},
		})
		require.NoError(t, err)
		s := stmt.New("DELETE FROM users WHERE id = ?", 42)
		mock.ExpectExec("DELETE FROM users WHERE id = ?").
			WithArgs(42).
			WillReturnResult(sqlmock.NewResult(0, 1))

		_, err = db.ExecRendered(t.Context(), s)
		require.ErrorIs(t, err, expected)
		require.ErrorContains(t, err, "hook after exec")
	})
}

func TestHooksRunInsideExplicitTransaction(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	events := make([]string, 0, 2)
	hook := exec.HookFunc{
		BeforeFunc: func(_ context.Context, operation exec.Operation) error {
			events = append(events, "before "+operation.Kind().String())
			return nil
		},
		AfterFunc: func(_ context.Context, operation exec.Operation, err error) error {
			require.NoError(t, err)
			events = append(events, "after "+operation.Kind().String())
			return nil
		},
	}
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE users SET email = ? WHERE id = ?").
		WithArgs("grace@example.com", 42).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	db, err := exec.New(database, dialect.SQLite())
	require.NoError(t, err)
	tx, err := db.Begin(t.Context(), nil)
	require.NoError(t, err)
	tx, err = tx.WithHooks(hook)
	require.NoError(t, err)
	s := stmt.New("UPDATE users SET email = ? WHERE id = ?", "grace@example.com", 42)
	_, err = tx.ExecRendered(t.Context(), s)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.Equal(t, []string{"before exec", "after exec"}, events)
}
