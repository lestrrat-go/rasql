package rasql

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestLogicalInvocation(t *testing.T) {
	t.Run("uses fresh statement indexes and one terminal", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		db, err := New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := AsExecutor(db, profile)
		require.NoError(t, err)
		var mu sync.Mutex
		var events []Event
		executor, err = WithEventObservers(executor, ExtensionErrorHandlerFunc(func(context.Context, ExtensionError) {}), EventObserverFunc(func(ctx context.Context, event Event) (context.Context, EventCompletion) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
			return ctx, EventCompletionFunc(func(_ context.Context, terminal Event) error {
				mu.Lock()
				events = append(events, terminal)
				mu.Unlock()
				return nil
			})
		}))
		require.NoError(t, err)
		for invocation := 0; invocation < 2; invocation++ {
			ctx, observed, completion := beginLogicalInvocation(t.Context(), executor, EventMutationBatch)
			for range 3 {
				_, err = observed.Exec(ctx, stmt.New("SELECT 1"))
				require.NoError(t, err)
			}
			completion.completeLogicalInvocation(nil, 0, false)
			completion.completeLogicalInvocation(assertionError{}, 0, false)
		}
		mu.Lock()
		defer mu.Unlock()
		require.Len(t, events, 16)
		for invocation := 0; invocation < 2; invocation++ {
			base := invocation * 8
			logical := events[base]
			require.Equal(t, EventMutationBatch, logical.Kind)
			require.Equal(t, EventStart, logical.Phase)
			for index := 0; index < 3; index++ {
				start := events[base+1+index*2]
				terminal := events[base+2+index*2]
				require.Equal(t, EventStatement, start.Kind)
				require.Equal(t, index, start.StatementIndex)
				require.Equal(t, logical.LogicalID, start.ParentID)
				require.Equal(t, start.LogicalID, terminal.LogicalID)
				require.Equal(t, index, terminal.StatementIndex)
			}
			logicalTerminal := events[base+7]
			require.Equal(t, EventMutationBatch, logicalTerminal.Kind)
			require.Equal(t, EventTerminal, logicalTerminal.Phase)
			require.Equal(t, logical.LogicalID, logicalTerminal.LogicalID)
		}
	})

	t.Run("without a provider it allocates nothing", func(t *testing.T) {
		type contextKey struct{}
		ctx := context.WithValue(context.Background(), contextKey{}, true)
		executor := capabilityTestExecutor{}
		derived, observed, completion := beginLogicalInvocation(ctx, executor, EventGraph)
		require.Same(t, ctx, derived)
		require.Equal(t, executor, observed)
		completion.completeLogicalInvocation(assertionError{}, 1, true)
	})

	t.Run("concurrent invocations use independent counters", func(t *testing.T) {
		base := capabilityTestExecutor{}
		var mu sync.Mutex
		indexes := make(map[string][]int)
		observed, err := WithEventObservers(base, ExtensionErrorHandlerFunc(func(context.Context, ExtensionError) {}), EventObserverFunc(func(ctx context.Context, event Event) (context.Context, EventCompletion) {
			if event.Kind == EventStatement && event.Phase == EventStart {
				mu.Lock()
				indexes[event.ParentID] = append(indexes[event.ParentID], event.StatementIndex)
				mu.Unlock()
			}
			return ctx, nil
		}))
		require.NoError(t, err)
		var wg sync.WaitGroup
		for range 2 {
			ctx, child, completion := beginLogicalInvocation(t.Context(), observed, EventGraph)
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = child.Exec(ctx, stmt.New("SELECT 1"))
				_, _ = child.Exec(ctx, stmt.New("SELECT 1"))
				completion.completeLogicalInvocation(nil, 0, false)
			}()
		}
		wg.Wait()
		mu.Lock()
		defer mu.Unlock()
		for _, values := range indexes {
			require.Equal(t, []int{0, 1}, values)
		}
		require.Len(t, indexes, 2)
	})

	t.Run("a nested invocation uses the outer parent", func(t *testing.T) {
		base := capabilityTestExecutor{}
		var starts []Event
		observed, err := WithEventObservers(base, ExtensionErrorHandlerFunc(func(context.Context, ExtensionError) {}), EventObserverFunc(func(ctx context.Context, event Event) (context.Context, EventCompletion) {
			if event.Phase == EventStart {
				starts = append(starts, event)
			}
			return ctx, nil
		}))
		require.NoError(t, err)
		_, outer, outerCompletion := beginLogicalInvocation(t.Context(), observed, EventGraph)
		_, inner, innerCompletion := beginLogicalInvocation(t.Context(), outer, EventGraph)
		_, err = inner.Exec(t.Context(), stmt.New("SELECT 1"))
		require.NoError(t, err)
		innerCompletion.completeLogicalInvocation(nil, 0, false)
		outerCompletion.completeLogicalInvocation(nil, 0, false)
		require.Len(t, starts, 3)
		require.Equal(t, starts[0].LogicalID, starts[1].ParentID)
		require.Equal(t, starts[1].LogicalID, starts[2].ParentID)
	})

	t.Run("chains contexts and reverses completions", func(t *testing.T) {
		type key string
		var order []string
		observed, err := WithEventObservers(capabilityTestExecutor{}, ExtensionErrorHandlerFunc(func(context.Context, ExtensionError) {}),
			EventObserverFunc(func(ctx context.Context, event Event) (context.Context, EventCompletion) {
				return context.WithValue(ctx, key("first"), true), EventCompletionFunc(func(ctx context.Context, event Event) error {
					require.True(t, ctx.Value(key("second")).(bool))
					order = append(order, "first")
					return nil
				})
			}),
			EventObserverFunc(func(ctx context.Context, event Event) (context.Context, EventCompletion) {
				require.True(t, ctx.Value(key("first")).(bool))
				return context.WithValue(ctx, key("second"), true), EventCompletionFunc(func(ctx context.Context, event Event) error {
					order = append(order, "second")
					return nil
				})
			}),
		)
		require.NoError(t, err)
		ctx, child, completion := beginLogicalInvocation(context.Background(), observed, EventGraph)
		_, err = child.Exec(ctx, stmt.New("SELECT 1"))
		require.NoError(t, err)
		completion.completeLogicalInvocation(nil, 0, false)
		require.Equal(t, []string{"second", "first", "second", "first"}, order)
	})

	t.Run("an observer failure never changes the application result", func(t *testing.T) {
		applicationErr := assertionError{}
		observed, err := WithEventObservers(capabilityTestExecutor{}, ExtensionErrorHandlerFunc(func(context.Context, ExtensionError) { panic("handler") }),
			EventObserverFunc(func(context.Context, Event) (context.Context, EventCompletion) { panic(applicationErr) }),
			EventObserverFunc(func(ctx context.Context, event Event) (context.Context, EventCompletion) {
				return ctx, EventCompletionFunc(func(context.Context, Event) error { panic("completion") })
			}),
		)
		require.NoError(t, err)
		ctx, child, completion := beginLogicalInvocation(context.Background(), observed, EventGraph)
		_, err = child.Exec(ctx, stmt.New("SELECT 1"))
		require.NoError(t, err)
		completion.completeLogicalInvocation(applicationErr, 0, false)
	})

	t.Run("nested invocations have independent indexes", func(t *testing.T) {
		var starts []Event
		observed, err := WithEventObservers(capabilityTestExecutor{}, ExtensionErrorHandlerFunc(func(context.Context, ExtensionError) {}), EventObserverFunc(func(ctx context.Context, event Event) (context.Context, EventCompletion) {
			if event.Kind == EventStatement && event.Phase == EventStart {
				starts = append(starts, event)
			}
			return ctx, nil
		}))
		require.NoError(t, err)
		ctx, outer, outerCompletion := beginLogicalInvocation(context.Background(), observed, EventGraph)
		_, err = outer.Exec(ctx, stmt.New("SELECT 1"))
		require.NoError(t, err)
		innerCtx, inner, innerCompletion := beginLogicalInvocation(ctx, outer, EventGraph)
		_, err = inner.Exec(innerCtx, stmt.New("SELECT 1"))
		require.NoError(t, err)
		_, err = inner.Exec(innerCtx, stmt.New("SELECT 1"))
		require.NoError(t, err)
		innerCompletion.completeLogicalInvocation(nil, 0, false)
		_, err = outer.Exec(ctx, stmt.New("SELECT 1"))
		require.NoError(t, err)
		outerCompletion.completeLogicalInvocation(nil, 0, false)
		require.Equal(t, []int{0, 0, 1, 1}, []int{starts[0].StatementIndex, starts[1].StatementIndex, starts[2].StatementIndex, starts[3].StatementIndex})
		require.Equal(t, starts[0].ParentID, starts[3].ParentID)
		require.NotEqual(t, starts[0].ParentID, starts[1].ParentID)
	})
}

type assertionError struct{}

func (assertionError) Error() string { return "assertion error" }
