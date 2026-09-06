package exec_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	defer func() { _ = rows.Finish(nil, true) }()
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
	mock.ExpectExec("UPDATE users SET name = ?").WithArgs("ada").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
	tx, err := db.Begin(t.Context(), nil)
	require.NoError(t, err)
	_, err = tx.ExecRendered(t.Context(), stmt.New("UPDATE users SET name = ?", "ada"))
	require.NoError(t, err)
	require.Len(t, phases, 2)
	require.Equal(t, exec.ExecutionPhase, phases[1].Phase)
	require.ErrorContains(t, tx.Commit(), "commit transaction")
	require.Len(t, phases, 3)
	require.Equal(t, exec.TransactionPhase, phases[0].Phase)
	require.Equal(t, exec.BeginOperation, phases[0].Operation.Kind())
	require.Equal(t, exec.ExecOperation, phases[1].Operation.Kind())
	require.Equal(t, exec.CommitOperation, phases[2].Operation.Kind())
	require.ErrorContains(t, phases[2].Err, "commit transaction")
}

func TestInvocationObserversPropagateContextAndReverseCompletion(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	type key string
	var starts []string
	var completions []string
	var hookContext string
	db, err := exec.New(database, dialect.SQLite(), exec.HookFunc{BeforeFunc: func(ctx context.Context, _ exec.Operation) error {
		hookContext, _ = ctx.Value(key("second")).(string)
		return nil
	}})
	require.NoError(t, err)
	db, err = db.WithInvocationObservers(exec.ExtensionErrorHandlerFunc(func(context.Context, exec.ExtensionError) {}),
		exec.InvocationObserverFunc(func(ctx context.Context, _ exec.Operation) (context.Context, exec.CompletionObserver) {
			starts = append(starts, "first")
			return context.WithValue(ctx, key("first"), "one"), exec.CompletionObserverFunc(func(ctx context.Context, _ exec.Completion) error {
				completions = append(completions, "first")
				_, _ = ctx.Value(key("second")).(string)
				return nil
			})
		}),
		exec.InvocationObserverFunc(func(ctx context.Context, _ exec.Operation) (context.Context, exec.CompletionObserver) {
			require.Equal(t, "one", ctx.Value(key("first")))
			starts = append(starts, "second")
			return context.WithValue(ctx, key("second"), "two"), exec.CompletionObserverFunc(func(context.Context, exec.Completion) error {
				completions = append(completions, "second")
				return nil
			})
		}))
	require.NoError(t, err)
	mock.ExpectExec("UPDATE users SET name = ?").WithArgs("ada").WillReturnResult(sqlmock.NewResult(0, 1))
	_, err = db.ExecRendered(t.Context(), stmt.New("UPDATE users SET name = ?", "ada"))
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, starts)
	require.Equal(t, []string{"second", "first"}, completions)
	require.Equal(t, "two", hookContext)
}

func TestRawQueryRenderedReportsExecutionOnlyAndOwnedPreservesHooks(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	var phases []exec.Phase
	hooks := 0
	db, err := exec.New(database, dialect.SQLite(), exec.HookFunc{BeforeFunc: func(_ context.Context, _ exec.Operation) error {
		hooks++
		return nil
	}})
	require.NoError(t, err)
	db, err = db.WithInvocationObservers(exec.ExtensionErrorHandlerFunc(func(context.Context, exec.ExtensionError) {}), exec.InvocationObserverFunc(func(ctx context.Context, _ exec.Operation) (context.Context, exec.CompletionObserver) {
		return ctx, exec.CompletionObserverFunc(func(_ context.Context, value exec.Completion) error {
			phases = append(phases, value.Phase)
			return nil
		})
	}))
	require.NoError(t, err)
	mock.ExpectQuery("SELECT id").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	raw, err := db.QueryRendered(t.Context(), stmt.New("SELECT id"))
	require.NoError(t, err)
	require.Equal(t, 1, hooks)
	require.True(t, raw.Next())
	require.NoError(t, raw.Close())
	require.Equal(t, []exec.Phase{exec.ExecutionPhase}, phases)

	mock.ExpectQuery("SELECT id").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	owned, err := db.QueryOwned(t.Context(), stmt.New("SELECT id"))
	require.NoError(t, err)
	require.NoError(t, owned.Close())
	require.Equal(t, 2, hooks)
	require.Equal(t, []exec.Phase{exec.ExecutionPhase, exec.ExecutionPhase, exec.ConsumptionPhase}, phases)
}

func TestConsumptionDurationIncludesPostExecutionWork(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	var consumption exec.Completion
	db, err := exec.New(database, dialect.SQLite())
	require.NoError(t, err)
	db, err = db.WithInvocationObservers(exec.ExtensionErrorHandlerFunc(func(context.Context, exec.ExtensionError) {}), exec.InvocationObserverFunc(func(ctx context.Context, _ exec.Operation) (context.Context, exec.CompletionObserver) {
		return ctx, exec.CompletionObserverFunc(func(_ context.Context, value exec.Completion) error {
			if value.Phase == exec.ConsumptionPhase {
				consumption = value
			}
			return nil
		})
	}))
	require.NoError(t, err)
	mock.ExpectQuery("SELECT id").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	rows, err := db.QueryOwned(t.Context(), stmt.New("SELECT id"))
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond)
	require.NoError(t, rows.Close())
	require.Greater(t, consumption.Finished.Sub(consumption.Started), time.Millisecond)
}

func TestTransactionBeginFailureAndRollbackNormalization(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	var completions []exec.Completion
	db, err := exec.New(database, dialect.SQLite())
	require.NoError(t, err)
	db, err = db.WithInvocationObservers(exec.ExtensionErrorHandlerFunc(func(context.Context, exec.ExtensionError) {}), exec.InvocationObserverFunc(func(ctx context.Context, _ exec.Operation) (context.Context, exec.CompletionObserver) {
		return ctx, exec.CompletionObserverFunc(func(_ context.Context, value exec.Completion) error {
			completions = append(completions, value)
			return nil
		})
	}))
	require.NoError(t, err)
	mock.ExpectBegin().WillReturnError(errors.New("begin failed"))
	_, err = db.Begin(t.Context(), nil)
	require.ErrorContains(t, err, "begin transaction")
	require.Len(t, completions, 1)
	require.Equal(t, exec.BeginOperation, completions[0].Operation.Kind())
	require.ErrorContains(t, completions[0].Err, "begin transaction")

	mock.ExpectBegin()
	mock.ExpectRollback()
	tx, err := db.Begin(t.Context(), nil)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	require.Len(t, completions, 3)
	require.Equal(t, exec.RollbackOperation, completions[2].Operation.Kind())
	require.NoError(t, completions[2].Err)
	// A second rollback normalizes database/sql's sql.ErrTxDone to nil and
	// still emits one transaction completion for the attempted call.
	require.NoError(t, tx.Rollback())
	require.Len(t, completions, 4)
	require.NoError(t, completions[3].Err)
}

func TestRowsEarlyCloseAndIterationError(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	var completions []exec.Completion
	db, err := exec.New(database, dialect.SQLite())
	require.NoError(t, err)
	db, err = db.WithInvocationObservers(exec.ExtensionErrorHandlerFunc(func(context.Context, exec.ExtensionError) {}), exec.InvocationObserverFunc(func(ctx context.Context, _ exec.Operation) (context.Context, exec.CompletionObserver) {
		return ctx, exec.CompletionObserverFunc(func(_ context.Context, value exec.Completion) error {
			if value.Phase == exec.ConsumptionPhase {
				completions = append(completions, value)
			}
			return nil
		})
	}))
	require.NoError(t, err)
	mock.ExpectQuery("SELECT id").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1).AddRow(2))
	rows, err := db.QueryOwned(t.Context(), stmt.New("SELECT id"))
	require.NoError(t, err)
	require.True(t, rows.Next())
	require.NoError(t, rows.Close())
	require.Len(t, completions, 1)
	require.True(t, completions[0].EarlyClose)

	iterationErr := errors.New("delayed iteration failure")
	mock.ExpectQuery("SELECT id").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1).AddRow(2).RowError(1, iterationErr))
	rows, err = db.QueryOwned(t.Context(), stmt.New("SELECT id"))
	require.NoError(t, err)
	require.True(t, rows.Next())
	require.False(t, rows.Next())
	require.ErrorIs(t, rows.Finish(nil, true), iterationErr)
	require.Len(t, completions, 2)
	require.ErrorIs(t, completions[1].Err, iterationErr)
	require.False(t, completions[1].EarlyClose)
}

func TestCompletionErrorsGoOnlyToHandler(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	var reported []error
	db, err := exec.New(database, dialect.SQLite())
	require.NoError(t, err)
	db, err = db.WithInvocationObservers(exec.ExtensionErrorHandlerFunc(func(_ context.Context, value exec.ExtensionError) {
		reported = append(reported, value.Errors...)
	}), exec.InvocationObserverFunc(func(ctx context.Context, _ exec.Operation) (context.Context, exec.CompletionObserver) {
		return ctx, exec.CompletionObserverFunc(func(context.Context, exec.Completion) error {
			return errors.New("completion reporter failed")
		})
	}))
	require.NoError(t, err)
	mock.ExpectExec("DELETE FROM users").WillReturnResult(sqlmock.NewResult(0, 1))
	_, err = db.ExecRendered(t.Context(), stmt.New("DELETE FROM users"))
	require.NoError(t, err)
	require.Len(t, reported, 1)
	require.ErrorContains(t, reported[0], "completion reporter failed")
}

func TestDelayedDriverSeparatesExecutionAndConsumptionDuration(t *testing.T) {
	database := openLifecycleDatabase(t, lifecycleDriverConfig{queryDelay: 2 * time.Millisecond, nextDelay: 5 * time.Millisecond}, nil)
	db, err := exec.New(database, dialect.SQLite())
	require.NoError(t, err)
	var completions []exec.Completion
	db, err = db.WithInvocationObservers(exec.ExtensionErrorHandlerFunc(func(context.Context, exec.ExtensionError) {}), exec.InvocationObserverFunc(func(ctx context.Context, _ exec.Operation) (context.Context, exec.CompletionObserver) {
		return ctx, exec.CompletionObserverFunc(func(_ context.Context, value exec.Completion) error {
			completions = append(completions, value)
			return nil
		})
	}))
	require.NoError(t, err)
	rows, err := db.QueryOwned(t.Context(), stmt.New("SELECT value"))
	require.NoError(t, err)
	require.True(t, rows.Next())
	require.NoError(t, rows.Close())
	require.Len(t, completions, 2)
	executionDuration := completions[0].Finished.Sub(completions[0].Started)
	consumptionDuration := completions[1].Finished.Sub(completions[1].Started)
	require.Greater(t, executionDuration, time.Millisecond)
	require.Greater(t, consumptionDuration, executionDuration)
}

func TestConcurrentInvocationsKeepDerivedMarkersPaired(t *testing.T) {
	recorder := &lifecycleDriverRecorder{}
	database := openLifecycleDatabase(t, lifecycleDriverConfig{}, recorder)
	db, err := exec.New(database, dialect.SQLite())
	require.NoError(t, err)
	var next atomic.Int64
	var mu sync.Mutex
	executionMarkers := make([]string, 0, 2)
	consumptionMarkers := make([]string, 0, 2)
	db, err = db.WithInvocationObservers(exec.ExtensionErrorHandlerFunc(func(context.Context, exec.ExtensionError) {}), exec.InvocationObserverFunc(func(ctx context.Context, _ exec.Operation) (context.Context, exec.CompletionObserver) {
		marker := "marker-" + fmt.Sprint(next.Add(1))
		return context.WithValue(ctx, lifecycleMarkerKey{}, marker), exec.CompletionObserverFunc(func(ctx context.Context, value exec.Completion) error {
			mu.Lock()
			if value.Phase == exec.ExecutionPhase {
				executionMarkers = append(executionMarkers, ctx.Value(lifecycleMarkerKey{}).(string))
			} else {
				consumptionMarkers = append(consumptionMarkers, ctx.Value(lifecycleMarkerKey{}).(string))
			}
			mu.Unlock()
			return nil
		})
	}))
	require.NoError(t, err)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			rows, queryErr := db.QueryOwned(t.Context(), stmt.New("SELECT value"))
			if queryErr != nil {
				return
			}
			_ = rows.Close()
		}()
	}
	group.Wait()
	recorder.mu.Lock()
	markers := append([]string(nil), recorder.markers...)
	recorder.mu.Unlock()
	require.Len(t, markers, 2)
	require.ElementsMatch(t, markers, executionMarkers)
	require.Len(t, consumptionMarkers, 2)
	require.NotEqual(t, executionMarkers, consumptionMarkers)
}

type lifecycleKey struct{}
