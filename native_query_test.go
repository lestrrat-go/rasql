package rasql

import (
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/stretchr/testify/require"
)

func nativeRuntimeQuery(t *testing.T, card Cardinality) Query[int64] {
	t.Helper()
	q, err := Native(NativeStatement{Engine: "sqlite", SQL: "SELECT value"}, runtimeQuery(t).Projection(), card)
	require.NoError(t, err)
	return q
}

func TestNativeConstructorCopiesArgumentsAndValidatesInputs(t *testing.T) {
	projection := runtimeQuery(t).Projection()
	args := []NativeArgument{{Value: []byte("one")}}
	q, err := Native(NativeStatement{Engine: "sqlite", SQL: "SELECT ?", Args: args}, projection, Many)
	require.NoError(t, err)
	args[0].Value.([]byte)[0] = 'x'
	require.NoError(t, q.Validate())
	for _, tc := range []struct {
		name string
		stmt NativeStatement
		card Cardinality
		code string
	}{
		{"empty engine", NativeStatement{SQL: "SELECT 1"}, Many, "invalid_projection"},
		{"blank sql", NativeStatement{Engine: "sqlite", SQL: "  "}, Many, "invalid_projection"},
		{"zero cardinality", NativeStatement{Engine: "sqlite", SQL: "SELECT 1"}, 0, "invalid_projection"},
		{"invalid codec", NativeStatement{Engine: "sqlite", SQL: "SELECT 1", Args: []NativeArgument{{Codec: "bad codec"}}}, Many, "invalid_schema"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Native(tc.stmt, projection, tc.card)
			var planErr *PlanError
			require.ErrorAs(t, err, &planErr)
			require.Equal(t, tc.code, planErr.Code)
		})
	}
}

func TestNativeEngineMismatchMakesZeroQueryCalls(t *testing.T) {
	raw := &runtimeFakeExecutor{dialect: dialect.PostgreSQL()}
	profile, err := EngineProfileFromVersion("postgresql-17", 17, 6, 0)
	require.NoError(t, err)
	executor, err := WithEngineProfile(raw, profile)
	require.NoError(t, err)
	_, err = All(t.Context(), executor, nativeRuntimeQuery(t, Many))
	var planErr *PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "engine_mismatch", planErr.Code)
	require.Equal(t, int64(0), raw.calls.Load())
}

func TestNativeCompositionRefusesEveryModifier(t *testing.T) {
	q := nativeRuntimeQuery(t, Many)
	other := runtimeQuery(t)
	checks := []struct {
		name  string
		check func() error
	}{
		{"where", func() error { return q.Where(Predicate{node: query.IsNull(query.Bind(nil))}).Validate() }},
		{"join", func() error {
			return q.Join(other.Plan().sources[0], Predicate{node: query.IsNull(query.Bind(nil))}).Validate()
		}},
		{"group", func() error { return q.GroupBy(Group(Value(1))).Validate() }},
		{"having", func() error { return q.Having(Predicate{node: query.IsNull(query.Bind(nil))}).Validate() }},
		{"order", func() error { return q.OrderBy(AscExpr(Value(1))).Validate() }},
		{"distinct", func() error { return q.Distinct().Validate() }},
		{"limit", func() error {
			changed, err := q.Limit(1)
			if err != nil {
				return err
			}
			return changed.Validate()
		}},
		{"offset", func() error {
			changed, err := q.Offset(1)
			if err != nil {
				return err
			}
			return changed.Validate()
		}},
		{"project", func() error { return Project(q.Plan(), q.Projection()).Validate() }},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			var planErr *PlanError
			require.ErrorAs(t, tc.check(), &planErr)
			require.Equal(t, "unsupported_feature", planErr.Code)
			require.Equal(t, "native", planErr.Path)
		})
	}
	_, err := Derive(q, "d")
	require.Error(t, err)
	_, err = CTEOf("c", q)
	require.Error(t, err)
	_, err = Combine(q, UnionAll, q)
	require.Error(t, err)
	require.Error(t, CountQuery(q, false).Validate())
}

func TestNativeDeclaredAndConsumerCardinalityShareOneLifecycle(t *testing.T) {
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	for _, tc := range []struct {
		name     string
		declared Cardinality
		consumer Cardinality
		rows     [][]any
		wantErr  error
		wantRows int
	}{
		{"many zero", Many, Many, nil, nil, 0},
		{"many two", Many, Many, [][]any{{int64(1)}, {int64(2)}}, nil, 2},
		{"at most one two", AtMostOne, Many, [][]any{{int64(1)}, {int64(2)}}, ErrMultipleRows, 1},
		{"exactly one zero", ExactlyOne, Many, nil, ErrNoRows, 0},
		{"one consumer two", Many, ExactlyOne, [][]any{{int64(1)}, {int64(2)}}, ErrMultipleRows, 1},
		{"maybe consumer two", Many, AtMostOne, [][]any{{int64(1)}, {int64(2)}}, ErrMultipleRows, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := &runtimeFakeExecutor{rows: tc.rows, dialect: dialect.SQLite()}
			executor, err := WithEngineProfile(raw, profile)
			require.NoError(t, err)
			q := nativeRuntimeQuery(t, tc.declared)
			var gotErr error
			switch tc.consumer {
			case ExactlyOne:
				_, gotErr = One(t.Context(), executor, q)
			case AtMostOne:
				_, _, gotErr = Maybe(t.Context(), executor, q)
			default:
				_, allErr := All(t.Context(), executor, q)
				gotErr = allErr
			}
			if tc.wantErr != nil {
				require.ErrorIs(t, gotErr, tc.wantErr)
			} else {
				require.NoError(t, gotErr)
			}
			require.Equal(t, tc.wantRows, gotOrRecorded(raw))
			require.NotNil(t, raw.last)
			require.Equal(t, 1, raw.last.finished)
			require.Equal(t, 1, raw.last.closed)
			if tc.wantErr != nil {
				require.True(t, errors.Is(raw.last.lastFinish, tc.wantErr))
			}
		})
	}
}

func TestNativeRowsBreakClosesWithoutCardinalityError(t *testing.T) {
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	raw := &runtimeFakeExecutor{rows: [][]any{{int64(1)}, {int64(2)}}, dialect: dialect.SQLite()}
	executor, err := WithEngineProfile(raw, profile)
	require.NoError(t, err)
	rows, err := Rows(t.Context(), executor, nativeRuntimeQuery(t, Many))
	require.NoError(t, err)
	for _, err := range rows {
		require.NoError(t, err)
		break
	}
	require.Equal(t, 1, raw.last.finished)
	require.Equal(t, 1, raw.last.closed)
	require.NoError(t, raw.last.lastFinish)
}

func gotOrRecorded(raw *runtimeFakeExecutor) int {
	if raw.last == nil {
		return 0
	}
	return raw.last.recorded
}
