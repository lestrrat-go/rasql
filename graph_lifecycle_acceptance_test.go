package rasql

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

type lifecycleExecutor struct {
	Executor
	compiler *querycompile.Compiler
	rows     []*runtimeFakeRows
	calls    atomic.Int64
	onQuery  func(context.Context)
}

func (e *lifecycleExecutor) queryCompiler() *querycompile.Compiler { return e.compiler }
func (e *lifecycleExecutor) Query(ctx context.Context, statement stmt.Statement) (ResultRows, error) {
	index := int(e.calls.Add(1)) - 1
	if e.onQuery != nil {
		e.onQuery(ctx)
	}
	if index >= len(e.rows) {
		return nil, errors.New("unexpected later graph query")
	}
	return e.rows[index], nil
}

func lifecycleGraphPlan(t *testing.T, base Executor, one bool, attach func(*graphParent, LoadedMany[graphChild])) GraphPlan[graphParentRow, graphParent] {
	t.Helper()
	_, parentSource, childSource, _, parentQuery, childQuery := graphAcceptanceFixture(t, 1)
	parents := TypedRelation[graphParentRow]{source: parentSource}
	children := TypedRelation[graphChildRow]{source: childSource}
	parentID, err := BindColumn[graphParentRow, int64](parents, "id", "")
	require.NoError(t, err)
	parentTenant, err := BindNullColumn[graphParentRow, int64](parents, "tenant", "")
	require.NoError(t, err)
	childParent, err := BindColumn[graphChildRow, int64](children, "parent", "")
	require.NoError(t, err)
	childTenant, err := BindColumn[graphChildRow, int64](children, "tenant", "")
	require.NoError(t, err)
	parentKey, err := NewGraphKey(KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }), NullKeyPart(parentTenant, func(row graphParentRow) Nullable[int64] { return row.Tenant }))
	require.NoError(t, err)
	childKey, err := NewGraphKey(KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }), KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }))
	require.NoError(t, err)
	childPlan, err := NewGraphPlan(childQuery, func(row graphChildRow) graphChild { return graphChild{ID: row.ID, Rank: row.Rank} })
	require.NoError(t, err)
	edge, err := HasMany("children", parentKey, childKey, childPlan, EdgeOptions{}, attach)
	if one {
		edge, err = HasOne("child", parentKey, childKey, childPlan, EdgeOptions{}, func(parent *graphParent, loaded LoadedOne[graphChild]) {
			if loaded.Present {
				attach(parent, LoadedMany[graphChild]{Loaded: loaded.Loaded, Values: []graphChild{*loaded.Value}})
				return
			}
			attach(parent, LoadedMany[graphChild]{Loaded: loaded.Loaded})
		})
	}
	require.NoError(t, err)
	limitedParent, err := parentQuery.Limit(1)
	require.NoError(t, err)
	plan, err := NewGraphPlan(limitedParent, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, edge)
	require.NoError(t, err)
	return plan
}

func TestGraphLifecycleEmitsOrderedLogicalAndStatementEvents(t *testing.T) {
	executor, parentSource, childSource, _, parentQuery, childQuery := graphAcceptanceFixture(t, 4)
	parents := TypedRelation[graphParentRow]{source: parentSource}
	children := TypedRelation[graphChildRow]{source: childSource}
	parentID, err := BindColumn[graphParentRow, int64](parents, "id", "")
	require.NoError(t, err)
	parentTenant, err := BindNullColumn[graphParentRow, int64](parents, "tenant", "")
	require.NoError(t, err)
	childParent, err := BindColumn[graphChildRow, int64](children, "parent", "")
	require.NoError(t, err)
	childTenant, err := BindColumn[graphChildRow, int64](children, "tenant", "")
	require.NoError(t, err)
	parentKey, err := NewGraphKey(KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }), NullKeyPart(parentTenant, func(row graphParentRow) Nullable[int64] { return row.Tenant }))
	require.NoError(t, err)
	childKey, err := NewGraphKey(KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }), KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }))
	require.NoError(t, err)
	childPlan, err := NewGraphPlan(childQuery, func(row graphChildRow) graphChild { return graphChild{ID: row.ID} })
	require.NoError(t, err)
	edge, err := HasMany("children", parentKey, childKey, childPlan, EdgeOptions{PerParentLimit: 2}, func(parent *graphParent, loaded LoadedMany[graphChild]) { parent.Children = loaded })
	require.NoError(t, err)
	plan, err := NewGraphPlan(parentQuery, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, edge)
	require.NoError(t, err)
	type contextKey struct{}
	var marker contextKey
	var mu sync.Mutex
	var events []Event
	terminalDerived := false
	var observerError = errors.New("observer completion failed")
	executor, err = WithEventObservers(executor,
		ExtensionErrorHandlerFunc(func(context.Context, ExtensionError) {}),
		EventObserverFunc(func(ctx context.Context, event Event) (context.Context, EventCompletion) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
			return context.WithValue(ctx, marker, true), EventCompletionFunc(func(completionCtx context.Context, terminal Event) error {
				if completionCtx.Value(marker) == true {
					terminalDerived = true
				}
				mu.Lock()
				events = append(events, terminal)
				mu.Unlock()
				if terminal.Kind == EventGraph && terminal.Phase == EventTerminal && terminal.Err == nil {
					return observerError
				}
				return nil
			})
		}),
	)
	require.NoError(t, err)
	values, err := LoadGraph(t.Context(), executor, plan)
	require.NoError(t, err)
	require.Len(t, values, 1)

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, events)
	require.Equal(t, EventGraph, events[0].Kind)
	require.Equal(t, EventStart, events[0].Phase)
	graphID := events[0].LogicalID
	var statementStarts, statementTerminals int
	for _, event := range events {
		if event.Kind == EventStatement && event.Phase == EventStart {
			require.Equal(t, graphID, event.ParentID)
			require.Equal(t, statementStarts, event.StatementIndex)
			statementStarts++
		}
		if event.Kind == EventStatement && event.Phase == EventTerminal {
			statementTerminals++
		}
	}
	require.Positive(t, statementStarts)
	require.Equal(t, statementStarts, statementTerminals)
	require.True(t, terminalDerived)
}

func TestGraphLifecycleDecodeFailureStopsLaterQueriesAndJoinsCleanup(t *testing.T) {
	base, _, _, _, _, _ := graphAcceptanceFixture(t, 1)
	provider, ok := base.(compilerProvider)
	require.True(t, ok)
	closeErr := errors.New("child close")
	finishErr := errors.New("child finish")
	childRows := &runtimeFakeRows{values: [][]any{{int64(11)}}, closeErr: closeErr, finishErr: finishErr}
	// The short child row makes the real decoder return the primary scan error.
	executor := &lifecycleExecutor{Executor: base, compiler: provider.queryCompiler(), rows: []*runtimeFakeRows{
		{values: [][]any{{int64(1), nil}}}, childRows,
	}}
	plan := lifecycleGraphPlan(t, base, false, func(*graphParent, LoadedMany[graphChild]) {})
	_, err := LoadGraph(t.Context(), executor, plan)
	require.Error(t, err)
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.ErrorIs(t, err, closeErr)
	require.ErrorIs(t, err, finishErr)
	require.Equal(t, int64(2), executor.calls.Load())
	require.Equal(t, 1, childRows.closed)
	require.Equal(t, 1, childRows.finished)
}

func TestGraphLifecycleCancellationStopsLaterBreadthLevels(t *testing.T) {
	base, _, _, _, _, _ := graphAcceptanceFixture(t, 1)
	provider, ok := base.(compilerProvider)
	require.True(t, ok)
	cancelCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	executor := &lifecycleExecutor{Executor: base, compiler: provider.queryCompiler(), rows: []*runtimeFakeRows{
		{values: [][]any{{int64(1), nil}}}, {values: [][]any{{int64(11), int64(1), int64(1), int64(0)}}},
	}}
	executor.onQuery = func(ctx context.Context) {
		if executor.calls.Load() == 2 {
			cancel()
		}
	}
	plan := lifecycleGraphPlan(t, base, false, func(*graphParent, LoadedMany[graphChild]) {})
	_, err := LoadGraph(cancelCtx, executor, plan)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, int64(2), executor.calls.Load())
}

func TestGraphLifecycleHasOneSecondRowStopsAttachment(t *testing.T) {
	base, _, _, _, _, _ := graphAcceptanceFixture(t, 1)
	provider, ok := base.(compilerProvider)
	require.True(t, ok)
	childRows := &runtimeFakeRows{values: [][]any{{int64(11), int64(1), int64(1), int64(0)}, {int64(12), int64(1), int64(1), int64(1)}}}
	executor := &lifecycleExecutor{Executor: base, compiler: provider.queryCompiler(), rows: []*runtimeFakeRows{
		{values: [][]any{{int64(1), nil}}}, childRows,
	}}
	plan := lifecycleGraphPlan(t, base, true, func(*graphParent, LoadedMany[graphChild]) {})
	_, err := LoadGraph(t.Context(), executor, plan)
	require.ErrorIs(t, err, ErrMultipleRows)
	require.Equal(t, int64(2), executor.calls.Load())
	require.Equal(t, 1, childRows.closed)
	require.Equal(t, 1, childRows.finished)
}

func TestGraphLifecycleMapperAndAttachmentPanicsPropagate(t *testing.T) {
	base, _, _, _, parentQuery, _ := graphAcceptanceFixture(t, 1)
	provider, ok := base.(compilerProvider)
	require.True(t, ok)
	newExecutor := func() *lifecycleExecutor {
		return &lifecycleExecutor{Executor: base, compiler: provider.queryCompiler(), rows: []*runtimeFakeRows{{values: [][]any{{int64(1), nil}}}}}
	}
	parents := parentQuery
	limitedParents, err := parents.Limit(1)
	require.NoError(t, err)
	mapperPlan, err := NewGraphPlan(limitedParents, func(graphParentRow) graphParent { panic("mapper panic") })
	require.NoError(t, err)
	require.Panics(t, func() { _, _ = LoadGraph(t.Context(), newExecutor(), mapperPlan) })
	plan := lifecycleGraphPlan(t, base, false, func(*graphParent, LoadedMany[graphChild]) { panic("attachment panic") })
	require.Panics(t, func() { _, _ = LoadGraph(t.Context(), newExecutor(), plan) })
}
