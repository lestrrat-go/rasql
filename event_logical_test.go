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

func TestLogicalInvocationUsesFreshStatementIndexesAndOneTerminal(t *testing.T) {
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
}

func TestLogicalInvocationWithoutProviderIsAllocationFreeNoop(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, true)
	executor := capabilityTestExecutor{}
	derived, observed, completion := beginLogicalInvocation(ctx, executor, EventGraph)
	require.Same(t, ctx, derived)
	require.Equal(t, executor, observed)
	completion.completeLogicalInvocation(assertionError{}, 1, true)
}

func TestLogicalInvocationsUseIndependentCountersConcurrently(t *testing.T) {
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
}

func TestNestedLogicalInvocationUsesOuterParent(t *testing.T) {
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
}

type assertionError struct{}

func (assertionError) Error() string { return "assertion error" }
