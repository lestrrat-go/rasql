package rasql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

var errNativeSnapshot = errors.New("native snapshot failed")

type nativeFailingSnapshotter struct{}

func (nativeFailingSnapshotter) SnapshotBind() (nativeFailingSnapshotter, error) {
	return nativeFailingSnapshotter{}, errNativeSnapshot
}

func nativeRuntimeQuery(t *testing.T, card Cardinality) Query[int64] {
	t.Helper()
	q, err := Native(NativeStatement{Engine: "sqlite", SQL: "SELECT value"}, runtimeQuery(t).Projection(), card)
	require.NoError(t, err)
	return q
}

func TestNativeQuery(t *testing.T) {
	t.Run("the constructor copies arguments and validates inputs", func(t *testing.T) {
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
	})

	t.Run("the constructor preserves a bind snapshot cause", func(t *testing.T) {
		projection := runtimeQuery(t).Projection()
		_, err := Native(NativeStatement{
			Engine: "sqlite",
			SQL:    "SELECT ?",
			Args:   []NativeArgument{{Value: nativeFailingSnapshotter{}}},
		}, projection, Many)
		require.Error(t, err)
		require.ErrorIs(t, err, errNativeSnapshot)
	})

	t.Run("an engine mismatch makes no query call", func(t *testing.T) {
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
	})

	t.Run("a derived query rejects $0 before taking a handle", func(t *testing.T) {
		projection := runtimeQuery(t).Projection()
		base, err := Native(NativeStatement{Engine: "postgresql", SQL: "SELECT $0 AS value"}, projection, Many)
		require.NoError(t, err)
		source, err := Derive(base, "native_values")
		require.NoError(t, err)
		value, err := BindResultColumn[int64, int64](source, "value")
		require.NoError(t, err)
		outer, err := Scalar("value", value.Expr(), schema.IntegerType{}, "")
		require.NoError(t, err)
		query := Select(source.Source(), outer)
		raw := &runtimeFakeExecutor{dialect: dialect.PostgreSQL()}
		profile, err := EngineProfileFromVersion("postgresql-17", 17, 6, 0)
		require.NoError(t, err)
		executor, err := WithEngineProfile(raw, profile)
		require.NoError(t, err)
		_, err = All(t.Context(), executor, query)
		var planErr *PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "invalid_query", planErr.Code)
		require.Equal(t, "native.sql", planErr.Path)
		require.Equal(t, int64(0), raw.calls.Load())
	})

	t.Run("composition refuses every modifier", func(t *testing.T) {
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
		for _, tc := range []struct {
			name string
			call func() error
		}{
			{"combine", func() error { _, err := Combine(q, UnionAll, q); return err }},
			{"with", func() error { _, err := With(q); return err }},
			{"count", func() error { return CountQuery(q, false).Validate() }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var planErr *PlanError
				err := tc.call()
				require.ErrorAs(t, err, &planErr)
				require.Equal(t, "unsupported_feature", planErr.Code)
				require.Equal(t, "native", planErr.Path)
			})
		}
	})

	t.Run("SELECT composition succeeds and DML composition fails", func(t *testing.T) {
		q := nativeRuntimeQuery(t, Many)
		derived, err := Derive(q, "derived_values")
		require.NoError(t, err)
		require.Equal(t, q.Schema().Columns(), derived.Source().ref.Columns())
		cte, err := CTEOf("native_values", q)
		require.NoError(t, err)
		_, err = cte.Source("native_alias")
		require.NoError(t, err)

		dml, err := Native(NativeStatement{Engine: "sqlite", SQL: "UPDATE values SET value = ? RETURNING value", Args: []NativeArgument{{Value: int64(1)}}}, q.Projection(), Many)
		require.NoError(t, err)
		for _, compose := range []func() error{
			func() error { _, err := Derive(dml, "dml_values"); return err },
			func() error { _, err := CTEOf("dml_values", dml); return err },
		} {
			var planErr *PlanError
			require.ErrorAs(t, compose(), &planErr)
			require.Equal(t, "unsupported_feature", planErr.Code)
			require.Equal(t, "native", planErr.Path)
		}
	})

	t.Run("a partition limit refuses before adoption", func(t *testing.T) {
		q := nativeRuntimeQuery(t, Many)
		changed, err := q.withPartitionLimit([]GroupKey{Group(Value(1))}, []OrderTerm{AscExpr(Value(1))}, 2)
		var planErr *PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "unsupported_feature", planErr.Code)
		require.Equal(t, "native", planErr.Path)
		require.NoError(t, q.Validate())
		require.NoError(t, changed.Validate())
	})

	t.Run("declared and consumer cardinality share one lifecycle", func(t *testing.T) {
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
	})

	t.Run("an early break checks the declared cardinality", func(t *testing.T) {
		profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		for _, tc := range []struct {
			name     string
			card     Cardinality
			rows     [][]any
			wantErr  error
			wantRows int
		}{
			{"at most zero", AtMostOne, nil, nil, 0},
			{"at most one", AtMostOne, [][]any{{int64(1)}}, nil, 1},
			{"at most two", AtMostOne, [][]any{{int64(1)}, {int64(2)}}, ErrMultipleRows, 0},
			{"exactly zero", ExactlyOne, nil, ErrNoRows, 0},
			{"exactly one", ExactlyOne, [][]any{{int64(1)}}, nil, 1},
			{"exactly two", ExactlyOne, [][]any{{int64(1)}, {int64(2)}}, ErrMultipleRows, 0},
		} {
			t.Run(tc.name, func(t *testing.T) {
				raw := &runtimeFakeExecutor{rows: tc.rows, dialect: dialect.SQLite()}
				executor, err := WithEngineProfile(raw, profile)
				require.NoError(t, err)
				sequence, err := Rows(t.Context(), executor, nativeRuntimeQuery(t, tc.card))
				require.NoError(t, err)
				var values []int64
				var gotErr error
				for value, rowErr := range sequence {
					if rowErr != nil {
						gotErr = rowErr
						break
					}
					values = append(values, value)
					break
				}
				if tc.wantErr == nil {
					require.NoError(t, gotErr)
				} else {
					require.ErrorIs(t, gotErr, tc.wantErr)
				}
				require.Len(t, values, tc.wantRows)
				require.NotNil(t, raw.last)
				require.Equal(t, 1, raw.last.finished)
				require.Equal(t, 1, raw.last.closed)
				require.Equal(t, tc.wantErr, raw.last.lastFinish)
			})
		}
	})

	t.Run("a break closes without a cardinality error", func(t *testing.T) {
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
	})
}

func gotOrRecorded(raw *runtimeFakeExecutor) int {
	if raw.last == nil {
		return 0
	}
	return raw.last.recorded
}

type nativeProjectionRow struct {
	ID    int64
	Value Nullable[string]
}

type nativeProjectionDecoder struct{ schema ResultSchema }

func (d nativeProjectionDecoder) ResultSchema() ResultSchema                     { return d.schema }
func (nativeProjectionDecoder) Presence() []Presence                             { return nil }
func (nativeProjectionDecoder) DecodeRow(ScanSource, *nativeProjectionRow) error { return nil }

func nativeProjectionTestDecoder(t *testing.T) nativeProjectionDecoder {
	t.Helper()
	sch, err := NewResultSchema(
		ResultColumn{Name: "id", Type: schema.IntegerType{}},
		ResultColumn{Name: "value", Type: schema.TextType{}, Nullable: true},
	)
	require.NoError(t, err)
	return nativeProjectionDecoder{schema: sch}
}

func TestNativeProjection(t *testing.T) {
	t.Run("marks the schema without binds", func(t *testing.T) {
		projection, err := NativeProjection(nativeProjectionTestDecoder(t))
		require.NoError(t, err)
		require.Len(t, projection.Schema().Columns(), 2)
		query, err := Native(NativeStatement{Engine: "sqlite", SQL: "SELECT 1 AS id, NULL AS value"}, projection, Many)
		require.NoError(t, err)
		require.NoError(t, query.Validate())
		require.Empty(t, query.plan.projection[0].expression)
		require.Empty(t, query.plan.projection[1].expression)
		ordinary := Select(runtimeQuery(t).Plan().sources[0], projection)
		var planErr *PlanError
		require.ErrorAs(t, ordinary.Validate(), &planErr)
		require.Equal(t, "unsupported_feature", planErr.Code)
		projected := Project(runtimeQuery(t).Plan(), projection)
		require.ErrorAs(t, projected.Validate(), &planErr)
		require.Equal(t, "unsupported_feature", planErr.Code)
	})

	t.Run("executes nullable custom codec columns", func(t *testing.T) {
		var encodes atomic.Int64
		var decodes atomic.Int64
		registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"custom": nullableBindCodec{encodeCalls: &encodes, decodeCalls: &decodes}})
		require.NoError(t, err)
		sch, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}}, ResultColumn{Name: "value", Type: schema.TextType{}, Nullable: true, Codec: "custom"})
		require.NoError(t, err)
		projection, err := NativeProjection(nativeMultiDecoder{schema: sch})
		require.NoError(t, err)
		query, err := Native(NativeStatement{Engine: "sqlite", SQL: "SELECT 7 AS id, 'x' AS value"}, projection, Many)
		require.NoError(t, err)
		raw := &nativeMultiExecutor{dialect: dialect.SQLite(), rows: &runtimeFakeRows{values: [][]any{{int64(7), "x"}}, columns: []string{"id", "value"}}}
		profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := WithEngineProfile(raw, profile)
		require.NoError(t, err)
		executor, err = WithCodecs(executor, registry)
		require.NoError(t, err)
		values, err := All(t.Context(), executor, query)
		require.NoError(t, err)
		require.Len(t, values, 1)
		require.Equal(t, int64(7), values[0].ID)
		require.True(t, values[0].Value.Valid)
		require.Equal(t, "x", values[0].Value.Value)
		require.Equal(t, int64(1), decodes.Load())
	})

	t.Run("rejects a typed nil, an invalid schema and bad presence", func(t *testing.T) {
		var typedNil *nativeProjectionDecoder
		_, err := NativeProjection[nativeProjectionRow](typedNil)
		var planErr *PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "invalid_projection", planErr.Code)
		invalid := nativeInvalidDecoder{}
		_, err = NativeProjection(invalid)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "invalid_schema", planErr.Code)
		badPresence := nativeBadPresenceDecoder{}
		_, err = NativeProjection(badPresence)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "invalid_projection", planErr.Code)
	})
}

func TestNativeArgument(t *testing.T) {
	t.Run("uses a nullable bind value", func(t *testing.T) {
		plan, err := newNativeQueryPlan(NativeStatement{Engine: "sqlite", SQL: "SELECT ?", Args: []NativeArgument{{Value: Nullable[int64]{}}}})
		require.NoError(t, err)
		args := plan.statement.Args()
		require.Len(t, args, 1)
		token, ok := args[0].(bindToken)
		require.True(t, ok)
		require.Nil(t, token.Value)
		present, err := newNativeQueryPlan(NativeStatement{Engine: "sqlite", SQL: "SELECT ?", Args: []NativeArgument{{Value: Nullable[int64]{Value: 0, Valid: true}}}})
		require.NoError(t, err)
		presentToken := present.statement.Args()[0].(bindToken)
		require.Equal(t, int64(0), presentToken.Value)
	})

	t.Run("canonical and custom values normalize once", func(t *testing.T) {
		var canonicalCalls atomic.Int64
		var customCalls atomic.Int64
		plan, err := newNativeQueryPlan(NativeStatement{Engine: "sqlite", SQL: "SELECT ?, ?", Args: []NativeArgument{
			{Value: Nullable[int64]{}},
			{Value: countedNullable{value: int64(0), valid: true, calls: &customCalls}},
		}})
		require.NoError(t, err)
		require.Equal(t, int64(0), canonicalCalls.Load())
		require.Equal(t, int64(1), customCalls.Load())
		args := plan.statement.Args()
		require.Nil(t, args[0].(bindToken).Value)
		require.Equal(t, int64(0), args[1].(bindToken).Value)
	})

	t.Run("a named null is preserved and skips the codec", func(t *testing.T) {
		var encodes atomic.Int64
		var decodes atomic.Int64
		registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"nullable": nullableBindCodec{encodeCalls: &encodes, decodeCalls: &decodes}})
		require.NoError(t, err)
		statement := stmt.New(sqltext.Text("SELECT ?"), sql.Named("value", nil))
		encoded, err := encodeStatement(statement, []bindSlot{{Codec: "nullable"}}, registry)
		require.NoError(t, err)
		require.Zero(t, encodes.Load())
		arg, ok := encoded.Args()[0].(sql.NamedArg)
		require.True(t, ok)
		require.Equal(t, "value", arg.Name)
		require.Nil(t, arg.Value)
	})

	t.Run("encoding skips the codec for nil arguments", func(t *testing.T) {
		var calls atomic.Int64
		registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"count": nativeProjectionCountingCodec{calls: &calls}})
		require.NoError(t, err)
		statement := stmt.New(sqltext.Text("SELECT ?, ?"), nil, "value")
		encoded, err := encodeStatement(statement, []bindSlot{{Codec: "count"}, {Codec: "count"}}, registry)
		require.NoError(t, err)
		require.Equal(t, int64(1), calls.Load())
		require.Nil(t, encoded.Args()[0])
		require.Equal(t, "value", encoded.Args()[1])
	})

	t.Run("repeated non-null codec occurrences encode once each", func(t *testing.T) {
		var encodes atomic.Int64
		var decodes atomic.Int64
		registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"nullable": nullableBindCodec{encodeCalls: &encodes, decodeCalls: &decodes}})
		require.NoError(t, err)
		statement := stmt.New(sqltext.Text("SELECT ?, ?"), int64(0), int64(0))
		_, err = encodeStatement(statement, []bindSlot{{Codec: "nullable"}, {Codec: "nullable"}}, registry)
		require.NoError(t, err)
		require.Equal(t, int64(2), encodes.Load())
	})

	t.Run("a missing selected codec fails before the executor", func(t *testing.T) {
		raw := &runtimeFakeExecutor{dialect: dialect.SQLite()}
		profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := WithEngineProfile(raw, profile)
		require.NoError(t, err)
		sch, err := NewResultSchema(ResultColumn{Name: "value", Type: schema.IntegerType{}, Codec: "missing"})
		require.NoError(t, err)
		decoder := runtimeDecoder{schema: sch}
		projection, err := NativeProjection(decoder)
		require.NoError(t, err)
		query, err := Native(NativeStatement{Engine: "sqlite", SQL: "SELECT 1 AS value"}, projection, Many)
		require.NoError(t, err)
		_, err = All(t.Context(), executor, query)
		var planErr *PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "codec_unavailable", planErr.Code)
		require.Zero(t, raw.calls.Load())
	})
}

type nativeProjectionCountingCodec struct{ calls *atomic.Int64 }

func (c nativeProjectionCountingCodec) Encode(value any) (driver.Value, error) {
	c.calls.Add(1)
	return value, nil
}
func (nativeProjectionCountingCodec) Decode(any, any) error { return nil }

type countedNullable struct {
	value any
	valid bool
	calls *atomic.Int64
}

func (v countedNullable) NullableBind() (any, bool) {
	v.calls.Add(1)
	return v.value, v.valid
}

type nullableBindCodec struct {
	encodeCalls *atomic.Int64
	decodeCalls *atomic.Int64
}

type nativeMultiExecutor struct {
	rows    *runtimeFakeRows
	dialect dialect.Dialect
}

func (e *nativeMultiExecutor) Dialect() dialect.Dialect { return e.dialect }
func (e *nativeMultiExecutor) Query(context.Context, stmt.Statement) (ResultRows, error) {
	return e.rows, nil
}
func (*nativeMultiExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	return driver.RowsAffected(0), nil
}

type nativeMultiDecoder struct{ schema ResultSchema }

func (d nativeMultiDecoder) ResultSchema() ResultSchema { return d.schema }
func (nativeMultiDecoder) Presence() []Presence         { return nil }
func (nativeMultiDecoder) DecodeRow(source ScanSource, value *nativeProjectionRow) error {
	return source.Scan(&value.ID, &value.Value)
}

func (c nullableBindCodec) Encode(value any) (driver.Value, error) {
	c.encodeCalls.Add(1)
	return value, nil
}
func (c nullableBindCodec) Decode(value any, destination any) error {
	c.decodeCalls.Add(1)
	return ScanValue(destination.(*string), value)
}

type nativeInvalidDecoder struct{}

func (nativeInvalidDecoder) ResultSchema() ResultSchema         { return ResultSchema{} }
func (nativeInvalidDecoder) Presence() []Presence               { return nil }
func (nativeInvalidDecoder) DecodeRow(ScanSource, *int64) error { return errors.New("unused") }

type nativeBadPresenceDecoder struct{}

func (nativeBadPresenceDecoder) ResultSchema() ResultSchema {
	sch, _ := NewResultSchema(ResultColumn{Name: "value", Type: schema.TextType{}, Nullable: true})
	return sch
}
func (nativeBadPresenceDecoder) Presence() []Presence {
	return []Presence{{component: "", columns: []string{"value"}}}
}
func (nativeBadPresenceDecoder) DecodeRow(ScanSource, *int64) error { return nil }

type nativeMutationExecutor struct {
	dialect      dialect.Dialect
	calls        atomic.Int64
	last         stmt.Statement
	err          error
	result       sql.Result
	returnNil    bool
	honorContext bool
}

func (e *nativeMutationExecutor) Dialect() dialect.Dialect { return e.dialect }
func (*nativeMutationExecutor) Query(context.Context, stmt.Statement) (ResultRows, error) {
	return nil, errors.New("unexpected query")
}
func (e *nativeMutationExecutor) Exec(ctx context.Context, statement stmt.Statement) (sql.Result, error) {
	e.calls.Add(1)
	e.last = statement
	if e.honorContext && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if e.err != nil {
		return nil, e.err
	}
	if e.returnNil {
		return nil, nil
	}
	if e.result != nil {
		return e.result, nil
	}
	return nativeMutationResult(3), nil
}

type nativeMutationResult int64

func (r nativeMutationResult) LastInsertId() (int64, error) { return 0, nil }
func (r nativeMutationResult) RowsAffected() (int64, error) { return int64(r), nil }

func TestNativeMutation(t *testing.T) {
	t.Run("shares native validation and copies arguments", func(t *testing.T) {
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
	})

	t.Run("an engine mismatch precedes the compiler and executor", func(t *testing.T) {
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
	})

	t.Run("RETURNING refuses before the projection is used", func(t *testing.T) {
		plan, err := NativeMutation(NativeStatement{Engine: "sqlite", SQL: "UPDATE users SET name = ?"})
		require.NoError(t, err)
		_, err = Returning(plan, runtimeQuery(t).Projection())
		var planErr *PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "unsupported_feature", planErr.Code)
		require.Equal(t, "native", planErr.Path)
	})

	t.Run("a batch rejects every native position before execution", func(t *testing.T) {
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
				_, err = ExecMutationBatch(t.Context(), executor, tc.plans(t), BulkOptions{Atomic: true})
				var planErr *PlanError
				require.ErrorAs(t, err, &planErr)
				require.Equal(t, "unsupported_feature", planErr.Code)
				require.Equal(t, tc.index, planErr.Path)
				require.Zero(t, raw.calls.Load())
			})
		}
	})

	t.Run("a batch rejects valid neighbors without executing", func(t *testing.T) {
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
	})

	t.Run("reports execution and result failures", func(t *testing.T) {
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
	})

	t.Run("encodes arguments once and copies statements", func(t *testing.T) {
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
	})

	t.Run("rejects a compiler failure and a missing codec before execution", func(t *testing.T) {
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
	})

	t.Run("reports a codec failure and cancellation", func(t *testing.T) {
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
	})
}

func nativeBatchPlan(t *testing.T) MutationPlan {
	t.Helper()
	plan, err := NativeMutation(NativeStatement{Engine: "sqlite", SQL: "UPDATE users SET name = ?", Args: []NativeArgument{{Value: "x"}}})
	require.NoError(t, err)
	return plan
}

func ordinaryBatchPlan(t *testing.T) MutationPlan {
	t.Helper()
	table, err := query.NewTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "name", Type: schema.TextType{}}}})
	require.NoError(t, err)
	statement, err := query.NewUpdate(table, query.Set(table.Column("name"), query.Bind("ordinary")))
	require.NoError(t, err)
	statement, err = statement.AllowAll()
	require.NoError(t, err)
	plan, err := NewStatementPlan(statement)
	require.NoError(t, err)
	return plan
}

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
