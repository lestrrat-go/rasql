package rasql_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/render"
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
	first := rasql.HookFunc{
		BeforeFunc: func(_ context.Context, operation rasql.Operation) error {
			events = append(events, "first before")
			require.Equal(t, rasql.ExecOperation, operation.Kind())
			require.Equal(t, "INSERT INTO users (email) VALUES ($1)", operation.SQL())
			args := operation.Args()
			require.Equal(t, []any{"ada@example.com"}, args)
			args[0] = "rewritten by hook"
			return nil
		},
		AfterFunc: func(_ context.Context, operation rasql.Operation, err error) error {
			events = append(events, "first after")
			require.NoError(t, err)
			require.Equal(t, []any{"ada@example.com"}, operation.Args())
			return nil
		},
	}
	second := rasql.HookFunc{
		BeforeFunc: func(_ context.Context, operation rasql.Operation) error {
			events = append(events, "second before")
			require.Equal(t, rasql.ExecOperation, operation.Kind())
			return nil
		},
		AfterFunc: func(_ context.Context, operation rasql.Operation, err error) error {
			events = append(events, "second after")
			require.NoError(t, err)
			require.Equal(t, "INSERT INTO users (email) VALUES ($1)", operation.SQL())
			return nil
		},
	}
	client, err := rasql.New(database, dialect.PostgreSQL(), first, second)
	require.NoError(t, err)
	statement, err := render.Precompiled("INSERT INTO users (email) VALUES ($1)", "ada@example.com")
	require.NoError(t, err)
	mock.ExpectExec("INSERT INTO users (email) VALUES ($1)").
		WithArgs("ada@example.com").
		WillReturnResult(sqlmock.NewResult(1, 1))

	_, err = client.ExecRendered(t.Context(), statement)
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

	var observed rasql.Operation
	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	client, err = client.WithHooks(rasql.HookFunc{
		BeforeFunc: func(_ context.Context, operation rasql.Operation) error {
			observed = operation
			return nil
		},
	})
	require.NoError(t, err)
	statement, err := render.Precompiled("SELECT id FROM users WHERE id = $1", 42)
	require.NoError(t, err)
	mock.ExpectQuery("SELECT id FROM users WHERE id = $1").
		WithArgs(42).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(42))

	rows, err := client.QueryRendered(t.Context(), statement)
	require.NoError(t, err)
	require.NoError(t, rows.Close())
	require.Equal(t, rasql.QueryOperation, observed.Kind())
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
		client, err := rasql.New(database, dialect.SQLite(), rasql.HookFunc{
			BeforeFunc: func(_ context.Context, operation rasql.Operation) error {
				require.Equal(t, rasql.ExecOperation, operation.Kind())
				return expected
			},
		})
		require.NoError(t, err)
		statement, err := render.Precompiled("DELETE FROM users")
		require.NoError(t, err)

		_, err = client.ExecRendered(t.Context(), statement)
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
		client, err := rasql.New(database, dialect.SQLite(), rasql.HookFunc{
			AfterFunc: func(_ context.Context, operation rasql.Operation, err error) error {
				require.NoError(t, err)
				require.Equal(t, rasql.ExecOperation, operation.Kind())
				return expected
			},
		})
		require.NoError(t, err)
		statement, err := render.Precompiled("DELETE FROM users WHERE id = ?", 42)
		require.NoError(t, err)
		mock.ExpectExec("DELETE FROM users WHERE id = ?").
			WithArgs(42).
			WillReturnResult(sqlmock.NewResult(0, 1))

		_, err = client.ExecRendered(t.Context(), statement)
		require.ErrorIs(t, err, expected)
		require.ErrorContains(t, err, "hook after exec")
	})
}

func TestTxHooksRunInsideExplicitTransaction(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	events := make([]string, 0, 2)
	hook := rasql.HookFunc{
		BeforeFunc: func(_ context.Context, operation rasql.Operation) error {
			events = append(events, "before "+operation.Kind().String())
			return nil
		},
		AfterFunc: func(_ context.Context, operation rasql.Operation, err error) error {
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

	tx, err := rasql.Begin(t.Context(), database, dialect.SQLite(), nil)
	require.NoError(t, err)
	tx, err = tx.WithHooks(hook)
	require.NoError(t, err)
	statement, err := render.Precompiled("UPDATE users SET email = ? WHERE id = ?", "grace@example.com", 42)
	require.NoError(t, err)
	_, err = tx.ExecRendered(t.Context(), statement)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.Equal(t, []string{"before exec", "after exec"}, events)
}
