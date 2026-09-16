package rasql_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestQueryCompletesExecutionAndConsumption(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	var mu sync.Mutex
	completions := make([]rasql.Completion, 0, 2)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	db, err = db.WithInvocationObservers(rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.InvocationObserverFunc(func(ctx context.Context, operation rasql.Operation) (context.Context, rasql.CompletionObserver) {
		derived := context.WithValue(ctx, lifecycleKey{}, operation.Kind().String())
		return derived, rasql.CompletionObserverFunc(func(_ context.Context, completion rasql.Completion) error {
			mu.Lock()
			completions = append(completions, completion)
			mu.Unlock()
			return nil
		})
	}))
	require.NoError(t, err)
	mock.ExpectQuery("SELECT id FROM users").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1).AddRow(2))
	rows, err := db.Query(t.Context(), stmt.New("SELECT id FROM users"))
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
	require.Equal(t, rasql.ExecutionPhase, completions[0].Phase)
	require.Equal(t, rasql.ConsumptionPhase, completions[1].Phase)
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
	var completion rasql.Completion
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	db, err = db.WithInvocationObservers(rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.InvocationObserverFunc(func(context.Context, rasql.Operation) (context.Context, rasql.CompletionObserver) {
		return context.Background(), rasql.CompletionObserverFunc(func(_ context.Context, value rasql.Completion) error {
			if value.Phase == rasql.ConsumptionPhase {
				completion = value
			}
			return nil
		})
	}))
	require.NoError(t, err)
	mock.ExpectQuery("SELECT value FROM values").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("bad"))
	rows, err := db.Query(t.Context(), stmt.New("SELECT value FROM values"))
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
	var phases []rasql.Completion
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	db, err = db.WithInvocationObservers(rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.InvocationObserverFunc(func(ctx context.Context, operation rasql.Operation) (context.Context, rasql.CompletionObserver) {
		return ctx, rasql.CompletionObserverFunc(func(_ context.Context, completion rasql.Completion) error {
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
	_, err = tx.Exec(t.Context(), stmt.New("UPDATE users SET name = ?", "ada"))
	require.NoError(t, err)
	require.Len(t, phases, 2)
	require.Equal(t, rasql.ExecutionPhase, phases[1].Phase)
	require.ErrorContains(t, tx.Commit(), "commit transaction")
	require.Len(t, phases, 3)
	require.Equal(t, rasql.TransactionPhase, phases[0].Phase)
	require.Equal(t, rasql.BeginOperation, phases[0].Operation.Kind())
	require.Equal(t, rasql.ExecOperation, phases[1].Operation.Kind())
	require.Equal(t, rasql.CommitOperation, phases[2].Operation.Kind())
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
	db, err := rasql.New(database, dialect.SQLite(), rasql.HookFunc{BeforeFunc: func(ctx context.Context, _ rasql.Operation) error {
		hookContext, _ = ctx.Value(key("second")).(string)
		return nil
	}})
	require.NoError(t, err)
	db, err = db.WithInvocationObservers(rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}),
		rasql.InvocationObserverFunc(func(ctx context.Context, _ rasql.Operation) (context.Context, rasql.CompletionObserver) {
			starts = append(starts, "first")
			return context.WithValue(ctx, key("first"), "one"), rasql.CompletionObserverFunc(func(ctx context.Context, _ rasql.Completion) error {
				completions = append(completions, "first")
				_, _ = ctx.Value(key("second")).(string)
				return nil
			})
		}),
		rasql.InvocationObserverFunc(func(ctx context.Context, _ rasql.Operation) (context.Context, rasql.CompletionObserver) {
			require.Equal(t, "one", ctx.Value(key("first")))
			starts = append(starts, "second")
			return context.WithValue(ctx, key("second"), "two"), rasql.CompletionObserverFunc(func(context.Context, rasql.Completion) error {
				completions = append(completions, "second")
				return nil
			})
		}))
	require.NoError(t, err)
	mock.ExpectExec("UPDATE users SET name = ?").WithArgs("ada").WillReturnResult(sqlmock.NewResult(0, 1))
	_, err = db.Exec(t.Context(), stmt.New("UPDATE users SET name = ?", "ada"))
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
	var phases []rasql.Phase
	hooks := 0
	db, err := rasql.New(database, dialect.SQLite(), rasql.HookFunc{BeforeFunc: func(_ context.Context, _ rasql.Operation) error {
		hooks++
		return nil
	}})
	require.NoError(t, err)
	db, err = db.WithInvocationObservers(rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.InvocationObserverFunc(func(ctx context.Context, _ rasql.Operation) (context.Context, rasql.CompletionObserver) {
		return ctx, rasql.CompletionObserverFunc(func(_ context.Context, value rasql.Completion) error {
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
	require.Equal(t, []rasql.Phase{rasql.ExecutionPhase}, phases)

	mock.ExpectQuery("SELECT id").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	owned, err := db.Query(t.Context(), stmt.New("SELECT id"))
	require.NoError(t, err)
	require.NoError(t, owned.Close())
	require.Equal(t, 2, hooks)
	require.Equal(t, []rasql.Phase{rasql.ExecutionPhase, rasql.ExecutionPhase, rasql.ConsumptionPhase}, phases)
}

func TestConsumptionDurationIncludesPostExecutionWork(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	var consumption rasql.Completion
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	db, err = db.WithInvocationObservers(rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.InvocationObserverFunc(func(ctx context.Context, _ rasql.Operation) (context.Context, rasql.CompletionObserver) {
		return ctx, rasql.CompletionObserverFunc(func(_ context.Context, value rasql.Completion) error {
			if value.Phase == rasql.ConsumptionPhase {
				consumption = value
			}
			return nil
		})
	}))
	require.NoError(t, err)
	mock.ExpectQuery("SELECT id").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	rows, err := db.Query(t.Context(), stmt.New("SELECT id"))
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
	var completions []rasql.Completion
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	db, err = db.WithInvocationObservers(rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.InvocationObserverFunc(func(ctx context.Context, _ rasql.Operation) (context.Context, rasql.CompletionObserver) {
		return ctx, rasql.CompletionObserverFunc(func(_ context.Context, value rasql.Completion) error {
			completions = append(completions, value)
			return nil
		})
	}))
	require.NoError(t, err)
	mock.ExpectBegin().WillReturnError(errors.New("begin failed"))
	_, err = db.Begin(t.Context(), nil)
	require.ErrorContains(t, err, "begin transaction")
	require.Len(t, completions, 1)
	require.Equal(t, rasql.BeginOperation, completions[0].Operation.Kind())
	require.ErrorContains(t, completions[0].Err, "begin transaction")

	mock.ExpectBegin()
	mock.ExpectRollback()
	tx, err := db.Begin(t.Context(), nil)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	require.Len(t, completions, 3)
	require.Equal(t, rasql.RollbackOperation, completions[2].Operation.Kind())
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
	var completions []rasql.Completion
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	db, err = db.WithInvocationObservers(rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.InvocationObserverFunc(func(ctx context.Context, _ rasql.Operation) (context.Context, rasql.CompletionObserver) {
		return ctx, rasql.CompletionObserverFunc(func(_ context.Context, value rasql.Completion) error {
			if value.Phase == rasql.ConsumptionPhase {
				completions = append(completions, value)
			}
			return nil
		})
	}))
	require.NoError(t, err)
	mock.ExpectQuery("SELECT id").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1).AddRow(2))
	rows, err := db.Query(t.Context(), stmt.New("SELECT id"))
	require.NoError(t, err)
	require.True(t, rows.Next())
	require.NoError(t, rows.Close())
	require.Len(t, completions, 1)
	require.True(t, completions[0].EarlyClose)

	iterationErr := errors.New("delayed iteration failure")
	mock.ExpectQuery("SELECT id").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1).AddRow(2).RowError(1, iterationErr))
	rows, err = db.Query(t.Context(), stmt.New("SELECT id"))
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
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	db, err = db.WithInvocationObservers(rasql.ExtensionErrorHandlerFunc(func(_ context.Context, value rasql.ExtensionError) {
		reported = append(reported, value.Errors...)
	}), rasql.InvocationObserverFunc(func(ctx context.Context, _ rasql.Operation) (context.Context, rasql.CompletionObserver) {
		return ctx, rasql.CompletionObserverFunc(func(context.Context, rasql.Completion) error {
			return errors.New("completion reporter failed")
		})
	}))
	require.NoError(t, err)
	mock.ExpectExec("DELETE FROM users").WillReturnResult(sqlmock.NewResult(0, 1))
	_, err = db.Exec(t.Context(), stmt.New("DELETE FROM users"))
	require.NoError(t, err)
	require.Len(t, reported, 1)
	require.ErrorContains(t, reported[0], "completion reporter failed")
}

func TestDelayedDriverSeparatesExecutionAndConsumptionDuration(t *testing.T) {
	queryStarted := make(chan struct{})
	queryRelease := make(chan struct{})
	nextStarted := make(chan struct{})
	nextRelease := make(chan struct{})
	recorder := &lifecycleDriverRecorder{}
	database := openLifecycleDatabase(t, lifecycleDriverConfig{
		queryStarted: queryStarted, queryRelease: queryRelease,
		nextStarted: nextStarted, nextRelease: nextRelease,
	}, recorder)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	var completions []rasql.Completion
	db, err = db.WithInvocationObservers(rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.InvocationObserverFunc(func(ctx context.Context, _ rasql.Operation) (context.Context, rasql.CompletionObserver) {
		return ctx, rasql.CompletionObserverFunc(func(_ context.Context, value rasql.Completion) error {
			completions = append(completions, value)
			return nil
		})
	}))
	require.NoError(t, err)
	type queryResult struct {
		rows rasql.ResultRows
		err  error
	}
	result := make(chan queryResult, 1)
	go func() {
		rows, queryErr := db.Query(t.Context(), stmt.New("SELECT value"))
		result <- queryResult{rows: rows, err: queryErr}
	}()
	<-queryStarted
	close(queryRelease)
	query := <-result
	require.NoError(t, query.err)
	rows := query.rows
	next := make(chan bool, 1)
	go func() { next <- rows.Next() }()
	<-nextStarted
	close(nextRelease)
	require.True(t, <-next)
	require.NoError(t, rows.Close())
	require.Len(t, completions, 2)
	executionDuration := completions[0].Finished.Sub(completions[0].Started)
	consumptionDuration := completions[1].Finished.Sub(completions[1].Started)
	recorder.mu.Lock()
	queryStartedAt := recorder.queryStarted
	queryFinishedAt := recorder.queryFinished
	nextStartedAt := recorder.nextStarted
	nextFinishedAt := recorder.nextFinished
	recorder.mu.Unlock()
	require.False(t, queryStartedAt.IsZero())
	require.False(t, queryFinishedAt.IsZero())
	require.False(t, nextStartedAt.IsZero())
	require.False(t, nextFinishedAt.IsZero())
	require.GreaterOrEqual(t, executionDuration, queryFinishedAt.Sub(queryStartedAt))
	require.GreaterOrEqual(t, consumptionDuration, nextFinishedAt.Sub(nextStartedAt))
	require.True(t, completions[0].Finished.Before(completions[1].Started))
}

func TestConcurrentInvocationsKeepDerivedMarkersPaired(t *testing.T) {
	recorder := &lifecycleDriverRecorder{}
	database := openLifecycleDatabase(t, lifecycleDriverConfig{}, recorder)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	var next atomic.Int64
	var mu sync.Mutex
	executionMarkers := make([]string, 0, 2)
	consumptionMarkers := make([]string, 0, 2)
	db, err = db.WithInvocationObservers(rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.InvocationObserverFunc(func(ctx context.Context, _ rasql.Operation) (context.Context, rasql.CompletionObserver) {
		marker := "marker-" + fmt.Sprint(next.Add(1))
		return context.WithValue(ctx, lifecycleMarkerKey{}, marker), rasql.CompletionObserverFunc(func(ctx context.Context, value rasql.Completion) error {
			mu.Lock()
			if value.Phase == rasql.ExecutionPhase {
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
			rows, queryErr := db.Query(t.Context(), stmt.New("SELECT value"))
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
