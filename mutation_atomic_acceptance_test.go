package rasql_test

import (
	"context"
	"sync"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/query"
	"github.com/stretchr/testify/require"
)

func TestMutationAtomicCommitAndRollbackLifecycle(t *testing.T) {
	for _, test := range []struct {
		name    string
		failure bool
	}{
		{name: "commit"}, {name: "rollback", failure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor, table, id := mutationFixture(t)
			value := queryTypedMutationValue(table)
			first, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(1)), rasql.SetField(value, "one"))
			require.NoError(t, err)
			plans := []rasql.MutationPlan{first}
			if test.failure {
				second, secondErr := rasql.NewCreatePlan(table, rasql.SetField(id, int64(1)), rasql.SetField(value, "duplicate"))
				require.NoError(t, secondErr)
				plans = append(plans, second)
			}
			var mu sync.Mutex
			events := make([]rasql.Event, 0, 8)
			executor, err = rasql.WithEventObservers(executor, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
				mu.Lock()
				events = append(events, event)
				mu.Unlock()
				return ctx, rasql.EventCompletionFunc(func(_ context.Context, terminal rasql.Event) error {
					mu.Lock()
					events = append(events, terminal)
					mu.Unlock()
					return nil
				})
			}))
			require.NoError(t, err)
			outcome, executionErr := rasql.ExecMutationBatch(t.Context(), executor, plans, rasql.BulkOptions{Atomic: true, MaxRows: 1, Classifier: mutationRejectClassifier{}})
			if test.failure {
				require.Error(t, executionErr)
				require.Equal(t, []rasql.InputOutcome{rasql.InputRolledBack, rasql.InputRejected}, outcome.Inputs)
			} else {
				require.NoError(t, executionErr)
				require.Equal(t, []rasql.InputOutcome{rasql.InputApplied}, outcome.Inputs)
			}
			mu.Lock()
			defer mu.Unlock()
			require.NotEmpty(t, events)
			var scopeTerminal, mutationTerminal int
			for index, event := range events {
				if event.Phase != rasql.EventTerminal {
					continue
				}
				if event.Kind == rasql.EventScope {
					scopeTerminal = index
				}
				if event.Kind == rasql.EventMutationBatch {
					mutationTerminal = index
				}
			}
			require.Greater(t, mutationTerminal, scopeTerminal)
		})
	}
}

func queryTypedMutationValue(table rasql.Table[mutationRow]) query.TypedColumn[mutationRow, string] {
	return query.TypedColumnOf[mutationRow, string](table.Column("value"))
}

func TestMutationAtomicPreflightRejectsNegativeLimitsWithoutExecution(t *testing.T) {
	executor, table, id := mutationFixture(t)
	value := queryTypedMutationValue(table)
	plan, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(1)), rasql.SetField(value, "one"))
	require.NoError(t, err)
	for _, options := range []rasql.BulkOptions{{Atomic: true, MaxRows: -1}, {Atomic: true, MaxBindParameters: -1}} {
		outcome, executionErr := rasql.ExecMutationBatch(t.Context(), executor, []rasql.MutationPlan{plan}, options)
		require.Error(t, executionErr)
		require.Equal(t, []rasql.InputOutcome{rasql.InputUnattempted}, outcome.Inputs)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	outcome, executionErr := rasql.ExecMutationBatch(ctx, executor, []rasql.MutationPlan{plan}, rasql.BulkOptions{Atomic: true})
	require.ErrorIs(t, executionErr, context.Canceled)
	require.Equal(t, []rasql.InputOutcome{rasql.InputUnattempted}, outcome.Inputs)
}
