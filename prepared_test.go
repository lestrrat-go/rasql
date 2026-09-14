package rasql_test

import (
	"context"
	"sync"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/stretchr/testify/require"
)

func TestPrepare(t *testing.T) {
	t.Run("executes many times with identical results", func(t *testing.T) {
		q := runtimeQuery(t)
		executor, raw := runtimeExecutor(t, [][]any{{int64(1)}, {int64(2)}})
		prepared, err := rasql.Prepare(executor, q)
		require.NoError(t, err)
		for i := 0; i < 3; i++ {
			values, err := prepared.All(t.Context(), executor)
			require.NoError(t, err)
			require.Equal(t, []int64{1, 2}, values)
		}
		require.Equal(t, int64(3), raw.calls.Load())
	})

	t.Run("each execution gets a fresh row sequence", func(t *testing.T) {
		q := runtimeQuery(t)
		executor, _ := runtimeExecutor(t, [][]any{{int64(1)}, {int64(2)}})
		prepared, err := rasql.Prepare(executor, q)
		require.NoError(t, err)
		for i := 0; i < 3; i++ {
			sequence, err := prepared.Rows(t.Context(), executor)
			require.NoError(t, err)
			var got []int64
			for value, rowErr := range sequence {
				require.NoError(t, rowErr)
				got = append(got, value)
			}
			require.Equal(t, []int64{1, 2}, got)
		}
	})

	t.Run("reports an error for a query that fails validation", func(t *testing.T) {
		executor, _ := runtimeExecutor(t, nil)
		_, err := rasql.Prepare(executor, rasql.Query[int64]{})
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "invalid_source", planErr.Code)
	})

	t.Run("reports a mismatch when run against a different executor", func(t *testing.T) {
		q := runtimeQuery(t)
		executorA, _ := runtimeExecutor(t, [][]any{{int64(1)}})
		executorB, _ := runtimeExecutor(t, [][]any{{int64(1)}})
		prepared, err := rasql.Prepare(executorA, q)
		require.NoError(t, err)

		var planErr *rasql.PlanError

		_, err = prepared.Rows(t.Context(), executorB)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "prepared_executor_mismatch", planErr.Code)

		_, err = prepared.All(t.Context(), executorB)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "prepared_executor_mismatch", planErr.Code)

		_, err = prepared.One(t.Context(), executorB)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "prepared_executor_mismatch", planErr.Code)

		_, _, err = prepared.Maybe(t.Context(), executorB)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "prepared_executor_mismatch", planErr.Code)
	})

	t.Run("is safe to execute from multiple goroutines", func(t *testing.T) {
		q := runtimeQuery(t)
		executor, raw := runtimeExecutor(t, [][]any{{int64(1)}})
		prepared, err := rasql.Prepare(executor, q)
		require.NoError(t, err)
		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				values, err := prepared.All(context.Background(), executor)
				require.NoError(t, err)
				require.Equal(t, []int64{1}, values)
			}()
		}
		wg.Wait()
		require.Equal(t, int64(100), raw.calls.Load())
	})
}
