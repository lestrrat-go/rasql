package rasql

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/stretchr/testify/require"
)

type nativeMutationRowsResult struct {
	rows int64
	err  error
}

func (r nativeMutationRowsResult) LastInsertId() (int64, error) { return 0, nil }
func (r nativeMutationRowsResult) RowsAffected() (int64, error) { return r.rows, r.err }

func nativeMutationExecutorForTest(t *testing.T) (Executor, *nativeMutationExecutor) {
	t.Helper()
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	raw := &nativeMutationExecutor{dialect: dialect.SQLite()}
	executor, err := WithEngineProfile(raw, profile)
	require.NoError(t, err)
	return executor, raw
}

func TestNativeMutationBatchRejectsValidNeighborsWithoutExecution(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plans func(*testing.T) []MutationPlan
		path  string
	}{
		{name: "first", plans: func(t *testing.T) []MutationPlan { return []MutationPlan{nativeBatchPlan(t), ordinaryBatchPlan(t)} }, path: "inputs[0]"},
		{name: "middle", plans: func(t *testing.T) []MutationPlan {
			return []MutationPlan{ordinaryBatchPlan(t), nativeBatchPlan(t), ordinaryBatchPlan(t)}
		}, path: "inputs[1]"},
		{name: "last", plans: func(t *testing.T) []MutationPlan { return []MutationPlan{ordinaryBatchPlan(t), nativeBatchPlan(t)} }, path: "inputs[1]"},
		{name: "singleton", plans: func(t *testing.T) []MutationPlan { return []MutationPlan{nativeBatchPlan(t)} }, path: "inputs[0]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor, raw := nativeMutationExecutorForTest(t)
			_, err := ExecMutationBatch(t.Context(), executor, tc.plans(t), BulkOptions{Atomic: true})
			var planErr *PlanError
			require.ErrorAs(t, err, &planErr)
			require.Equal(t, "unsupported_feature", planErr.Code)
			require.Equal(t, tc.path, planErr.Path)
			require.Zero(t, raw.calls.Load())
		})
	}
}

func TestNativeMutationReportsExecutionAndResultFailures(t *testing.T) {
	executionErr := errors.New("executor failed")
	rowsErr := errors.New("rows affected failed")
	for _, tc := range []struct {
		name    string
		setup   func(*nativeMutationExecutor)
		want    error
		unknown bool
	}{
		{name: "executor", setup: func(raw *nativeMutationExecutor) { raw.err = executionErr }, want: executionErr, unknown: true},
		{name: "nil result", setup: func(raw *nativeMutationExecutor) { raw.returnNil = true }, want: nil, unknown: true},
		{name: "rows affected", setup: func(raw *nativeMutationExecutor) { raw.result = nativeMutationRowsResult{rows: 2, err: rowsErr} }, want: rowsErr, unknown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor, raw := nativeMutationExecutorForTest(t)
			tc.setup(raw)
			plan, err := NativeMutation(NativeStatement{Engine: "sqlite", SQL: "UPDATE users SET name = ?", Args: []NativeArgument{{Value: "x"}}})
			require.NoError(t, err)
			outcome, err := ExecMutation(t.Context(), executor, plan)
			require.Error(t, err)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			}
			if tc.unknown {
				require.Equal(t, DurabilityUnknown, outcome.Durability)
			}
			require.EqualValues(t, 1, raw.calls.Load())
		})
	}
}

type nativeMutationCodec struct{ calls *int }

func (c nativeMutationCodec) Encode(value any) (driver.Value, error) {
	(*c.calls)++
	return "encoded:" + value.(string), nil
}
func (nativeMutationCodec) Decode(any, any) error { return nil }

var errNativeMutationCodec = errors.New("codec failed")

type nativeMutationFailingCodec struct{}

func (nativeMutationFailingCodec) Encode(any) (driver.Value, error) {
	return nil, errNativeMutationCodec
}
func (nativeMutationFailingCodec) Decode(any, any) error { return nil }

func TestNativeMutationEncodesArgumentsOnceAndCopiesStatements(t *testing.T) {
	count := 0
	registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"text": nativeMutationCodec{calls: &count}})
	require.NoError(t, err)
	executor, raw := nativeMutationExecutorForTest(t)
	executor, err = WithCodecs(executor, registry)
	require.NoError(t, err)
	plan, err := NativeMutation(NativeStatement{Engine: "sqlite", SQL: "UPDATE users SET name = ?", Args: []NativeArgument{{Value: "x", Codec: "text"}}})
	require.NoError(t, err)
	_, err = ExecMutation(t.Context(), executor, plan)
	require.NoError(t, err)
	first := raw.last
	_, err = ExecMutation(t.Context(), executor, plan)
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.NotSame(t, &first, &raw.last)
	require.Equal(t, "encoded:x", raw.last.Args()[0])
}

func TestNativeMutationRejectsCompilerAndMissingCodecBeforeExecution(t *testing.T) {
	for _, tc := range []struct {
		name string
		plan NativeStatement
		code string
	}{
		{name: "compiler", plan: NativeStatement{Engine: "sqlite", SQL: "UPDATE users SET"}, code: ""},
		{name: "missing codec", plan: NativeStatement{Engine: "sqlite", SQL: "UPDATE users SET name = ?", Args: []NativeArgument{{Value: "x", Codec: "missing"}}}, code: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor, raw := nativeMutationExecutorForTest(t)
			if tc.name == "compiler" {
				executor = raw
			}
			if tc.name == "missing codec" {
				registry, err := NewCodecRegistry(map[CodecID]ValueCodec{})
				require.NoError(t, err)
				executor, err = WithCodecs(executor, registry)
				require.NoError(t, err)
			}
			plan, err := NativeMutation(tc.plan)
			require.NoError(t, err)
			_, err = ExecMutation(context.Background(), executor, plan)
			require.Error(t, err)
			require.Zero(t, raw.calls.Load())
		})
	}
}

func TestNativeMutationReportsCodecFailureAndCancellation(t *testing.T) {
	executor, raw := nativeMutationExecutorForTest(t)
	registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"text": nativeMutationFailingCodec{}})
	require.NoError(t, err)
	executor, err = WithCodecs(executor, registry)
	require.NoError(t, err)
	plan, err := NativeMutation(NativeStatement{Engine: "sqlite", SQL: "UPDATE users SET name = ?", Args: []NativeArgument{{Value: "x", Codec: "text"}}})
	require.NoError(t, err)
	_, err = ExecMutation(t.Context(), executor, plan)
	require.ErrorIs(t, err, errNativeMutationCodec)
	require.Zero(t, raw.calls.Load())

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	raw.honorContext = true
	plain, err := NativeMutation(NativeStatement{Engine: "sqlite", SQL: "UPDATE users SET name = ?", Args: []NativeArgument{{Value: "x"}}})
	require.NoError(t, err)
	_, err = ExecMutation(cancelled, executor, plain)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualValues(t, 1, raw.calls.Load())
}
