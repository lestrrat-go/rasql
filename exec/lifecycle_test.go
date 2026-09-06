package exec_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/exec"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestQueryOwnedCompletesExecutionAndConsumption(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	var mu sync.Mutex
	completions := make([]exec.Completion, 0, 2)
	db, err := exec.New(database, dialect.SQLite())
	require.NoError(t, err)
	db, err = db.WithInvocationObservers(exec.ExtensionErrorHandlerFunc(func(context.Context, exec.ExtensionError) {}), exec.InvocationObserverFunc(func(ctx context.Context, operation exec.Operation) (context.Context, exec.CompletionObserver) {
		derived := context.WithValue(ctx, lifecycleKey{}, operation.Kind().String())
		return derived, exec.CompletionObserverFunc(func(_ context.Context, completion exec.Completion) error {
			mu.Lock()
			completions = append(completions, completion)
			mu.Unlock()
			return nil
		})
	}))
	require.NoError(t, err)
	mock.ExpectQuery("SELECT id FROM users").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1).AddRow(2))
	rows, err := db.QueryOwned(t.Context(), stmt.New("SELECT id FROM users"))
	require.NoError(t, err)
	defer rows.Finish(nil, true)
	var id int
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&id))
	require.Equal(t, 1, id)
	rows.RecordRow()
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&id))
	rows.RecordRow()
	require.False(t, rows.Next())
	require.NoError(t, rows.Finish(nil, true))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, completions, 2)
	require.Equal(t, exec.ExecutionPhase, completions[0].Phase)
	require.Equal(t, exec.ConsumptionPhase, completions[1].Phase)
	require.Equal(t, int64(2), completions[1].RowsRead)
	require.False(t, completions[1].EarlyClose)
}

func TestRowsFinishReportsConversionAndCardinalityErrors(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	var completion exec.Completion
	db, err := exec.New(database, dialect.SQLite())
	require.NoError(t, err)
	db, err = db.WithInvocationObservers(exec.ExtensionErrorHandlerFunc(func(context.Context, exec.ExtensionError) {}), exec.InvocationObserverFunc(func(context.Context, exec.Operation) (context.Context, exec.CompletionObserver) {
		return context.Background(), exec.CompletionObserverFunc(func(_ context.Context, value exec.Completion) error {
			if value.Phase == exec.ConsumptionPhase {
				completion = value
			}
			return nil
		})
	}))
	require.NoError(t, err)
	mock.ExpectQuery("SELECT value FROM values").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("bad"))
	rows, err := db.QueryOwned(t.Context(), stmt.New("SELECT value FROM values"))
	require.NoError(t, err)
	var value int
	require.True(t, rows.Next())
	scanErr := rows.Scan(&value)
	require.Error(t, scanErr)
	require.ErrorIs(t, rows.Finish(scanErr, true), scanErr)
	require.ErrorIs(t, completion.Err, scanErr)
	require.Equal(t, int64(0), completion.RowsRead)
}

func TestTransactionLifecycleReportsCommitFailure(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	var phases []exec.Completion
	db, err := exec.New(database, dialect.SQLite())
	require.NoError(t, err)
	db, err = db.WithInvocationObservers(exec.ExtensionErrorHandlerFunc(func(context.Context, exec.ExtensionError) {}), exec.InvocationObserverFunc(func(ctx context.Context, operation exec.Operation) (context.Context, exec.CompletionObserver) {
		return ctx, exec.CompletionObserverFunc(func(_ context.Context, completion exec.Completion) error {
			phases = append(phases, completion)
			return nil
		})
	}))
	require.NoError(t, err)
	mock.ExpectBegin()
	mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
	tx, err := db.Begin(t.Context(), nil)
	require.NoError(t, err)
	require.ErrorContains(t, tx.Commit(), "commit transaction")
	require.Len(t, phases, 2)
	require.Equal(t, exec.TransactionPhase, phases[0].Phase)
	require.Equal(t, exec.BeginOperation, phases[0].Operation.Kind())
	require.Equal(t, exec.CommitOperation, phases[1].Operation.Kind())
	require.ErrorContains(t, phases[1].Err, "commit transaction")
}

type lifecycleKey struct{}
