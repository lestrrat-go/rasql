package rasql_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
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

	t.Run("reports a mismatch when the executor is rewrapped with different codecs", func(t *testing.T) {
		q := runtimeQuery(t)
		executor, _ := runtimeExecutor(t, [][]any{{int64(1)}})
		registryA, err := rasql.NewCodecRegistry(nil)
		require.NoError(t, err)
		withA, err := rasql.WithCodecs(executor, registryA)
		require.NoError(t, err)
		prepared, err := rasql.Prepare(withA, q)
		require.NoError(t, err)

		registryB, err := rasql.NewCodecRegistry(nil)
		require.NoError(t, err)
		withB, err := rasql.WithCodecs(executor, registryB)
		require.NoError(t, err)

		var planErr *rasql.PlanError

		_, err = prepared.Rows(t.Context(), withB)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "prepared_executor_mismatch", planErr.Code)

		_, err = prepared.All(t.Context(), withB)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "prepared_executor_mismatch", planErr.Code)

		_, err = prepared.One(t.Context(), withB)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "prepared_executor_mismatch", planErr.Code)

		_, _, err = prepared.Maybe(t.Context(), withB)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "prepared_executor_mismatch", planErr.Code)

		// The registry Prepare actually captured still runs.
		values, err := prepared.All(t.Context(), withA)
		require.NoError(t, err)
		require.Equal(t, []int64{1}, values)
	})

	t.Run("keeps working inside a scope opened after Prepare", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		_, err = database.ExecContext(t.Context(), `CREATE TABLE items (value INTEGER)`)
		require.NoError(t, err)
		_, err = database.ExecContext(t.Context(), `INSERT INTO items VALUES (1), (2)`)
		require.NoError(t, err)
		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		base, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		registry, err := rasql.NewCodecRegistry(nil)
		require.NoError(t, err)
		executor, err := rasql.WithCodecs(base, registry)
		require.NoError(t, err)

		q := runtimeQuery(t)
		prepared, err := rasql.Prepare(executor, q)
		require.NoError(t, err)

		beginner, ok := executor.(rasql.ScopeBeginner)
		require.True(t, ok)
		child, finalizer, err := beginner.BeginScope(t.Context(), nil)
		require.NoError(t, err)
		values, err := prepared.All(t.Context(), child)
		require.NoError(t, err)
		require.Equal(t, []int64{1, 2}, values)
		require.NoError(t, finalizer.Rollback(t.Context()))
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
