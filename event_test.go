package rasql_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestEventObserver(t *testing.T) {
	t.Run("gets ordered scope and statement events", func(t *testing.T) {
		executor := sqliteExecutor(t)
		var mu sync.Mutex
		var events []rasql.Event
		type contextKey struct{}
		var marker contextKey
		terminalDerived := false
		executor, err := rasql.WithEventObservers(executor, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
			return context.WithValue(ctx, marker, true), rasql.EventCompletionFunc(func(completionCtx context.Context, terminal rasql.Event) error {
				if completionCtx.Value(marker) == true {
					terminalDerived = true
				}
				mu.Lock()
				events = append(events, terminal)
				mu.Unlock()
				return nil
			})
		}))
		require.NoError(t, err)
		require.NoError(t, rasql.Within(t.Context(), executor, nil, func(ctx context.Context, scoped rasql.Executor) error {
			if ctx.Value(marker) != true {
				return errors.New("derived scope context was not propagated")
			}
			_, err := scoped.Exec(ctx, stmt.New("INSERT INTO values_table VALUES (1)"))
			return err
		}))
		mu.Lock()
		defer mu.Unlock()
		require.Len(t, events, 4)
		require.Equal(t, rasql.EventScope, events[0].Kind)
		require.Equal(t, rasql.EventStart, events[0].Phase)
		require.Equal(t, events[0].LogicalID, events[1].ParentID)
		require.Equal(t, rasql.EventStatement, events[1].Kind)
		require.Equal(t, 0, events[1].StatementIndex)
		require.Equal(t, rasql.EventTerminal, events[2].Phase)
		require.Equal(t, events[1].LogicalID, events[2].LogicalID)
		require.Equal(t, events[0].LogicalID, events[3].LogicalID)
		require.True(t, terminalDerived)
	})

	t.Run("observer errors and panics never change results", func(t *testing.T) {
		executor := sqliteExecutor(t)
		var extensionErrors int
		var mu sync.Mutex
		panicValue := errors.New("observer start")
		sawPanic := false
		executor, err := rasql.WithEventObservers(executor, rasql.ExtensionErrorHandlerFunc(func(_ context.Context, extensionErr rasql.ExtensionError) {
			mu.Lock()
			extensionErrors++
			if errors.Is(&extensionErr, panicValue) {
				sawPanic = true
			}
			mu.Unlock()
		}), rasql.EventObserverFunc(func(context.Context, rasql.Event) (context.Context, rasql.EventCompletion) {
			panic(panicValue)
		}), rasql.EventObserverFunc(func(ctx context.Context, _ rasql.Event) (context.Context, rasql.EventCompletion) {
			return ctx, rasql.EventCompletionFunc(func(context.Context, rasql.Event) error { return errors.New("observer completion") })
		}))
		require.NoError(t, err)
		_, err = executor.Exec(t.Context(), stmt.New("INSERT INTO values_table VALUES (2)"))
		require.NoError(t, err)
		mu.Lock()
		defer mu.Unlock()
		require.GreaterOrEqual(t, extensionErrors, 2)
		require.True(t, sawPanic)
	})

	t.Run("a terminal preserves the statement error", func(t *testing.T) {
		executor := sqliteExecutor(t)
		var terminal rasql.Event
		var mu sync.Mutex
		executor, err := rasql.WithEventObservers(executor, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
			return ctx, rasql.EventCompletionFunc(func(_ context.Context, event rasql.Event) error {
				if event.Phase == rasql.EventTerminal {
					mu.Lock()
					terminal = event
					mu.Unlock()
				}
				return nil
			})
		}))
		require.NoError(t, err)
		_, err = executor.Exec(t.Context(), stmt.New("INSERT INTO missing_table VALUES (1)"))
		require.Error(t, err)
		mu.Lock()
		defer mu.Unlock()
		require.Error(t, terminal.Err)
	})

	t.Run("a handler panic cannot escape execution", func(t *testing.T) {
		executor := sqliteExecutor(t)
		var err error
		executor, err = rasql.WithEventObservers(executor, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {
			panic("handler panic")
		}), rasql.EventObserverFunc(func(ctx context.Context, _ rasql.Event) (context.Context, rasql.EventCompletion) {
			return ctx, rasql.EventCompletionFunc(func(context.Context, rasql.Event) error { return errors.New("observer error") })
		}))
		require.NoError(t, err)
		_, err = executor.Exec(t.Context(), stmt.New("INSERT INTO values_table VALUES (6)"))
		require.NoError(t, err)
	})

	t.Run("statement indexes are invocation-local", func(t *testing.T) {
		executor := sqliteExecutor(t)
		var mu sync.Mutex
		var starts []rasql.Event
		executor, err := rasql.WithEventObservers(executor, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
			if event.Kind == rasql.EventStatement && event.Phase == rasql.EventStart {
				mu.Lock()
				starts = append(starts, event)
				mu.Unlock()
			}
			return ctx, nil
		}))
		require.NoError(t, err)
		_, err = executor.Exec(t.Context(), stmt.New("INSERT INTO values_table VALUES (3)"))
		require.NoError(t, err)
		_, err = executor.Exec(t.Context(), stmt.New("INSERT INTO values_table VALUES (4)"))
		require.NoError(t, err)
		mu.Lock()
		defer mu.Unlock()
		require.Len(t, starts, 2)
		require.Equal(t, 0, starts[0].StatementIndex)
		require.Equal(t, 0, starts[1].StatementIndex)
	})

	t.Run("the rows count and exactly-once terminal", func(t *testing.T) {
		executor := sqliteExecutor(t)
		var mu sync.Mutex
		var terminals []rasql.Event
		executor, err := rasql.WithEventObservers(executor, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
			return ctx, rasql.EventCompletionFunc(func(_ context.Context, terminal rasql.Event) error {
				if terminal.Kind == rasql.EventStatement && terminal.Phase == rasql.EventTerminal {
					mu.Lock()
					terminals = append(terminals, terminal)
					mu.Unlock()
				}
				return nil
			})
		}))
		require.NoError(t, err)
		_, err = executor.Exec(t.Context(), stmt.New("INSERT INTO values_table VALUES (5)"))
		require.NoError(t, err)
		rows, err := executor.Query(t.Context(), stmt.New("SELECT value FROM values_table ORDER BY value"))
		require.NoError(t, err)
		require.True(t, rows.Next())
		rows.RecordRow()
		require.NoError(t, rows.Finish(nil, true))
		require.Error(t, rows.Finish(errors.New("ignored"), true))
		mu.Lock()
		defer mu.Unlock()
		require.Len(t, terminals, 2)
		require.Equal(t, int64(1), terminals[1].Rows)
		require.True(t, terminals[1].EarlyClose)
	})

	t.Run("a scope terminal joins callback and rollback errors", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()); require.NoError(t, mock.ExpectationsWereMet()) })
		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		var terminal rasql.Event
		executor, err = rasql.WithEventObservers(executor, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
			return ctx, rasql.EventCompletionFunc(func(_ context.Context, event rasql.Event) error {
				if event.Kind == rasql.EventScope && event.Phase == rasql.EventTerminal {
					terminal = event
				}
				return nil
			})
		}))
		require.NoError(t, err)
		callbackErr := errors.New("callback failure")
		rollbackErr := errors.New("rollback failure")
		mock.ExpectBegin()
		mock.ExpectRollback().WillReturnError(rollbackErr)
		mock.ExpectClose()
		err = rasql.Within(t.Context(), executor, nil, func(context.Context, rasql.Executor) error { return callbackErr })
		require.ErrorIs(t, err, callbackErr)
		require.ErrorIs(t, err, rollbackErr)
		require.ErrorIs(t, terminal.Err, callbackErr)
		require.ErrorIs(t, terminal.Err, rollbackErr)
	})
}

func sqliteExecutor(t *testing.T) rasql.Executor {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.ExecContext(t.Context(), "CREATE TABLE values_table (value INTEGER)")
	require.NoError(t, err)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	return executor
}
