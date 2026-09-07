package rasql

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

type nativeMutationExecutor struct {
	dialect dialect.Dialect
	calls   atomic.Int64
	last    stmt.Statement
}

func (e *nativeMutationExecutor) Dialect() dialect.Dialect { return e.dialect }
func (*nativeMutationExecutor) Query(context.Context, stmt.Statement) (ResultRows, error) {
	return nil, errors.New("unexpected query")
}
func (e *nativeMutationExecutor) Exec(_ context.Context, statement stmt.Statement) (sql.Result, error) {
	e.calls.Add(1)
	e.last = statement
	return nativeMutationResult(3), nil
}

type nativeMutationResult int64

func (r nativeMutationResult) LastInsertId() (int64, error) { return 0, nil }
func (r nativeMutationResult) RowsAffected() (int64, error) { return int64(r), nil }

func TestNativeMutationSharesNativeValidationAndCopiesArguments(t *testing.T) {
	args := []NativeArgument{{Value: []byte("one")}}
	plan, err := NativeMutation(NativeStatement{Engine: " sqlite ", SQL: "UPDATE users SET name = ?", Args: args})
	require.NoError(t, err)
	args[0].Value.([]byte)[0] = 'x'

	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	raw := &nativeMutationExecutor{dialect: dialect.SQLite()}
	executor, err := WithEngineProfile(raw, profile)
	require.NoError(t, err)
	outcome, err := ExecMutation(t.Context(), executor, plan)
	require.NoError(t, err)
	require.Equal(t, int64(3), outcome.Affected)
	require.Equal(t, "UPDATE users SET name = ?", raw.last.SQL())
	require.Equal(t, []byte("one"), raw.last.Args()[0])

	for _, tc := range []struct {
		name string
		stmt NativeStatement
		code string
		path string
	}{
		{name: "empty engine", stmt: NativeStatement{SQL: "UPDATE users SET name = ?"}, code: "invalid_projection", path: "native.engine"},
		{name: "blank sql", stmt: NativeStatement{Engine: "sqlite", SQL: "  "}, code: "invalid_projection", path: "native.sql"},
		{name: "invalid codec", stmt: NativeStatement{Engine: "sqlite", SQL: "UPDATE users SET name = ?", Args: []NativeArgument{{Codec: "bad codec"}}}, code: "invalid_schema", path: "native.args[0].codec"},
		{name: "unsnapshotable", stmt: NativeStatement{Engine: "sqlite", SQL: "UPDATE users SET name = ?", Args: []NativeArgument{{Value: nativeFailingSnapshotter{}}}}, code: "unsnapshotable_bind", path: "native.args[0]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NativeMutation(tc.stmt)
			var planErr *PlanError
			require.ErrorAs(t, err, &planErr)
			require.Equal(t, tc.code, planErr.Code)
			require.Equal(t, tc.path, planErr.Path)
		})
	}
}

func TestNativeMutationEngineMismatchPrecedesCompilerAndExecutor(t *testing.T) {
	plan, err := NativeMutation(NativeStatement{Engine: "sqlite", SQL: "UPDATE users SET name = ?", Args: []NativeArgument{{Value: "x"}}})
	require.NoError(t, err)
	profile, err := EngineProfileFromVersion("postgresql-17", 17, 6, 0)
	require.NoError(t, err)
	raw := &nativeMutationExecutor{dialect: dialect.PostgreSQL()}
	executor, err := WithEngineProfile(raw, profile)
	require.NoError(t, err)
	_, err = ExecMutation(t.Context(), executor, plan)
	var planErr *PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "engine_mismatch", planErr.Code)
	require.Zero(t, raw.calls.Load())
}

func TestNativeMutationReturningRefusesBeforeProjectionUse(t *testing.T) {
	plan, err := NativeMutation(NativeStatement{Engine: "sqlite", SQL: "UPDATE users SET name = ?"})
	require.NoError(t, err)
	_, err = Returning(plan, runtimeQuery(t).Projection())
	var planErr *PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "unsupported_feature", planErr.Code)
	require.Equal(t, "native", planErr.Path)
}

func TestNativeMutationBatchRejectsEveryNativePositionBeforeExecution(t *testing.T) {
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	for _, tc := range []struct {
		name  string
		plans func(t *testing.T) []MutationPlan
		index string
	}{
		{name: "first", plans: func(t *testing.T) []MutationPlan { return []MutationPlan{nativeBatchPlan(t), ordinaryBatchPlan(t)} }, index: "inputs[0]"},
		{name: "middle", plans: func(t *testing.T) []MutationPlan {
			return []MutationPlan{ordinaryBatchPlan(t), nativeBatchPlan(t), ordinaryBatchPlan(t)}
		}, index: "inputs[1]"},
		{name: "last", plans: func(t *testing.T) []MutationPlan { return []MutationPlan{ordinaryBatchPlan(t), nativeBatchPlan(t)} }, index: "inputs[1]"},
		{name: "singleton", plans: func(t *testing.T) []MutationPlan { return []MutationPlan{nativeBatchPlan(t)} }, index: "inputs[0]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := &nativeMutationExecutor{dialect: dialect.SQLite()}
			executor, err := WithEngineProfile(raw, profile)
			require.NoError(t, err)
			_, err = ExecMutationBatch(t.Context(), executor, tc.plans(t), MutationBatchOptions{Atomic: true})
			var planErr *PlanError
			require.ErrorAs(t, err, &planErr)
			require.Equal(t, "unsupported_feature", planErr.Code)
			require.Equal(t, tc.index, planErr.Path)
			require.Zero(t, raw.calls.Load())
		})
	}
}

func nativeBatchPlan(t *testing.T) MutationPlan {
	t.Helper()
	plan, err := NativeMutation(NativeStatement{Engine: "sqlite", SQL: "UPDATE users SET name = ?", Args: []NativeArgument{{Value: "x"}}})
	require.NoError(t, err)
	return plan
}

func ordinaryBatchPlan(t *testing.T) MutationPlan {
	t.Helper()
	plan, err := NewStatementPlan(nil)
	require.Error(t, err)
	return plan
}
