package rasql

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

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
