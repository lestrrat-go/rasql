//go:build unix

package rasql_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type nativeCompositionPair struct {
	First  int64
	Second int64
}

type nativeCompositionPairDecoder struct{ schema rasql.ResultSchema }

func (d nativeCompositionPairDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (nativeCompositionPairDecoder) Presence() []rasql.Presence         { return nil }
func (d nativeCompositionPairDecoder) DecodeRow(source rasql.ScanSource, value *nativeCompositionPair) error {
	return source.Scan(&value.First, &value.Second)
}

type nativeCompositionOuter struct {
	First  int64
	Marker int64
}

type nativeCompositionCodec struct {
	encode atomic.Int64
	decode atomic.Int64
	mu     sync.Mutex
	values []int64
}

func (c *nativeCompositionCodec) Encode(value any) (driver.Value, error) {
	integer, ok := value.(int64)
	if !ok {
		return nil, errors.New("native composition codec received a non-int64")
	}
	c.encode.Add(1)
	c.mu.Lock()
	c.values = append(c.values, integer)
	c.mu.Unlock()
	return integer, nil
}

func (c *nativeCompositionCodec) Decode(value any, destination any) error {
	integer, ok := value.(int64)
	if !ok {
		return errors.New("native composition codec received a non-int64 result")
	}
	c.decode.Add(1)
	*destination.(*int64) = integer
	return nil
}

func (c *nativeCompositionCodec) encodedValues() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int64(nil), c.values...)
}

func nativeCompositionSQLiteExecutor(t *testing.T, codecID string, codec rasql.ValueCodec) rasql.Executor {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	registry, err := rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{rasql.CodecID(codecID): codec})
	require.NoError(t, err)
	executor, err = rasql.WithCodecs(executor, registry)
	require.NoError(t, err)
	return executor
}

func nativeCompositionSQLiteQuery(t *testing.T, codecID string) rasql.Query[nativeCompositionPair] {
	t.Helper()
	resultSchema, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "first", Type: schema.IntegerType{}, Codec: codecID},
		rasql.ResultColumn{Name: "second", Type: schema.IntegerType{}, Codec: codecID},
	)
	require.NoError(t, err)
	projection, err := rasql.NativeProjection(nativeCompositionPairDecoder{schema: resultSchema})
	require.NoError(t, err)
	query, err := rasql.Native(rasql.NativeStatement{
		Engine: "sqlite",
		SQL:    "SELECT ? AS first, ? AS second",
		Args: []rasql.NativeArgument{
			{Value: int64(7), Codec: codecID},
			{Value: int64(7), Codec: codecID},
		},
	}, projection, rasql.Many)
	require.NoError(t, err)
	return query
}

func nativeCompositionOuterQuery(t *testing.T, source rasql.TypedSource[nativeCompositionPair], codecID string) rasql.Query[nativeCompositionOuter] {
	t.Helper()
	first, err := rasql.BindResultColumn[nativeCompositionPair, int64](source, "first")
	require.NoError(t, err)
	marker, err := rasql.ValueWithCodec[int64](int64(99), codecID)
	require.NoError(t, err)
	whereValue, err := rasql.ValueWithCodec[int64](int64(7), codecID)
	require.NoError(t, err)
	resultSchema, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "first", Type: schema.IntegerType{}, Codec: codecID},
		rasql.ResultColumn{Name: "marker", Type: schema.IntegerType{}, Codec: codecID},
	)
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("first", first.Expr(), schema.IntegerType{}, codecID),
		rasql.Item("marker", marker, schema.IntegerType{}, codecID),
	}, nativeCompositionOuterDecoder{schema: resultSchema})
	require.NoError(t, err)
	return rasql.Select(source.Source(), projection).Where(rasql.EqualExpr(first.Expr(), whereValue))
}

func TestNativeComposition(t *testing.T) {
	t.Run("a derived table and a CTE preserve argument order and codec counts on SQLite", func(t *testing.T) {
		codec := &nativeCompositionCodec{}
		executor := nativeCompositionSQLiteExecutor(t, "count", codec)
		base := nativeCompositionSQLiteQuery(t, "count")

		derived, err := rasql.Derive(base, "native_values")
		require.NoError(t, err)
		derivedQuery := nativeCompositionOuterQuery(t, derived, "count")
		values, err := rasql.All(t.Context(), executor, derivedQuery)
		require.NoError(t, err)
		require.Equal(t, []nativeCompositionOuter{{First: 7, Marker: 99}}, values)
		require.Equal(t, int64(4), codec.encode.Load())
		require.Equal(t, int64(2), codec.decode.Load())
		require.Equal(t, []int64{99, 7, 7, 7}, codec.encodedValues())

		codec.encode.Store(0)
		codec.decode.Store(0)
		codec.mu.Lock()
		codec.values = nil
		codec.mu.Unlock()
		cte, err := rasql.CTEOf("native_values", base)
		require.NoError(t, err)
		cteSource, err := cte.Source("native_alias")
		require.NoError(t, err)
		cteQuery := nativeCompositionOuterQuery(t, cteSource, "count")
		cteQuery, err = rasql.With(cteQuery, cte)
		require.NoError(t, err)
		values, err = rasql.All(t.Context(), executor, cteQuery)
		require.NoError(t, err)
		require.Equal(t, []nativeCompositionOuter{{First: 7, Marker: 99}}, values)
		require.Equal(t, int64(4), codec.encode.Load())
		require.Equal(t, int64(2), codec.decode.Load())
		require.Equal(t, []int64{7, 7, 99, 7}, codec.encodedValues())
	})

	t.Run("derived clones retain the adopted snapshot", func(t *testing.T) {
		calls := &atomic.Int64{}
		argument := &nativeCompositionSnapshotValue{Value: "before", Calls: calls}
		resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.TextType{}, Codec: "snapshot"})
		require.NoError(t, err)
		projection, err := rasql.NativeProjection(nativeCompositionStringDecoder{schema: resultSchema})
		require.NoError(t, err)
		base, err := rasql.Native(rasql.NativeStatement{
			Engine: "sqlite",
			SQL:    "SELECT ? AS value",
			Args:   []rasql.NativeArgument{{Value: argument, Codec: "snapshot"}},
		}, projection, rasql.Many)
		require.NoError(t, err)
		argument.Value = "after"
		first, err := rasql.Derive(base, "first_values")
		require.NoError(t, err)
		second, err := rasql.Derive(base, "second_values")
		require.NoError(t, err)
		executor := nativeCompositionSQLiteExecutor(t, "snapshot", nativeCompositionSnapshotCodec{})
		firstQuery := rasql.Select(first.Source(), projectionFromSnapshotSource(t, first))
		secondQuery := rasql.Select(second.Source(), projectionFromSnapshotSource(t, second))
		firstValues, err := rasql.All(t.Context(), executor, firstQuery)
		require.NoError(t, err)
		secondValues, err := rasql.All(t.Context(), executor, secondQuery)
		require.NoError(t, err)
		require.Equal(t, []string{"before"}, firstValues)
		require.Equal(t, []string{"before"}, secondValues)
		require.Equal(t, int64(1), calls.Load())
	})

	t.Run("the lifecycle uses one row source for a derived table and a CTE", func(t *testing.T) {
		decodeErr := errors.New("native decode failed")
		decodeFinishErr := errors.New("native decode finish failed")
		queryErr := errors.New("native executor failed")
		iterationErr := errors.New("native iteration failed")
		iterationFinishErr := errors.New("native iteration finish failed")
		for _, cte := range []bool{false, true} {
			shape := "derived"
			if cte {
				shape = "cte"
			}
			t.Run(shape, func(t *testing.T) {
				t.Run("success", func(t *testing.T) {
					rows := &nativeCompositionLifecycleRows{columns: []string{"value"}, values: [][]any{{int64(1)}, {int64(2)}}}
					executor := nativeCompositionLifecycleExecutor{dialect: dialect.SQLite(), rows: rows}
					profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
					require.NoError(t, err)
					profiled, err := rasql.WithEngineProfile(&executor, profile)
					require.NoError(t, err)
					values, err := rasql.All(t.Context(), profiled, nativeCompositionLifecycleQuery(t, cte))
					require.NoError(t, err)
					require.Equal(t, []int64{1, 2}, values)
					require.Equal(t, 1, rows.finishCalls)
					require.Equal(t, 1, rows.closeCalls)
					require.Equal(t, 2, rows.recorded)
					require.NoError(t, rows.primary)
					require.False(t, rows.early)
				})
				t.Run("decode failure", func(t *testing.T) {
					rows := &nativeCompositionLifecycleRows{
						columns: []string{"value"}, values: [][]any{{int64(1)}}, scanErr: decodeErr,
						finishErr: decodeFinishErr,
					}
					executor := nativeCompositionLifecycleExecutor{dialect: dialect.SQLite(), rows: rows}
					profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
					require.NoError(t, err)
					profiled, err := rasql.WithEngineProfile(&executor, profile)
					require.NoError(t, err)
					_, err = rasql.All(t.Context(), profiled, nativeCompositionLifecycleQuery(t, cte))
					require.ErrorIs(t, err, decodeErr)
					require.ErrorIs(t, err, decodeFinishErr)
					require.Equal(t, 1, rows.finishCalls)
					require.Equal(t, 1, rows.closeCalls)
					require.Equal(t, 0, rows.recorded)
					require.ErrorIs(t, rows.primary, decodeErr)
					require.True(t, rows.early)
				})
				t.Run("executor failure", func(t *testing.T) {
					rows := &nativeCompositionLifecycleRows{columns: []string{"value"}}
					executor := nativeCompositionLifecycleExecutor{dialect: dialect.SQLite(), rows: rows, queryErr: queryErr}
					profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
					require.NoError(t, err)
					profiled, err := rasql.WithEngineProfile(&executor, profile)
					require.NoError(t, err)
					_, err = rasql.All(t.Context(), profiled, nativeCompositionLifecycleQuery(t, cte))
					require.ErrorIs(t, err, queryErr)
					require.Equal(t, 0, rows.finishCalls)
					require.Equal(t, 0, rows.recorded)
				})
				t.Run("iteration failure", func(t *testing.T) {
					rows := &nativeCompositionLifecycleRows{
						columns: []string{"value"}, values: [][]any{{int64(1)}},
						iterationErr: iterationErr, finishErr: iterationFinishErr,
					}
					executor := nativeCompositionLifecycleExecutor{dialect: dialect.SQLite(), rows: rows}
					profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
					require.NoError(t, err)
					profiled, err := rasql.WithEngineProfile(&executor, profile)
					require.NoError(t, err)
					_, err = rasql.All(t.Context(), profiled, nativeCompositionLifecycleQuery(t, cte))
					require.ErrorIs(t, err, iterationErr)
					require.ErrorIs(t, err, iterationFinishErr)
					require.Equal(t, 1, rows.finishCalls)
					require.Equal(t, 1, rows.closeCalls)
					require.Equal(t, 1, rows.recorded)
					require.ErrorIs(t, rows.primary, iterationErr)
					require.False(t, rows.early)
				})
				t.Run("early close", func(t *testing.T) {
					rows := &nativeCompositionLifecycleRows{columns: []string{"value"}, values: [][]any{{int64(1)}, {int64(2)}}}
					executor := nativeCompositionLifecycleExecutor{dialect: dialect.SQLite(), rows: rows}
					profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
					require.NoError(t, err)
					profiled, err := rasql.WithEngineProfile(&executor, profile)
					require.NoError(t, err)
					sequence, err := rasql.Rows(t.Context(), profiled, nativeCompositionLifecycleQuery(t, cte))
					require.NoError(t, err)
					for value, rowErr := range sequence {
						require.NoError(t, rowErr)
						require.Equal(t, int64(1), value)
						break
					}
					require.Equal(t, 1, rows.finishCalls)
					require.Equal(t, 1, rows.closeCalls)
					require.Equal(t, 1, rows.recorded)
					require.NoError(t, rows.primary)
					require.True(t, rows.early)
				})
			})
		}
	})

	t.Run("an engine mismatch precedes the codec lookup and taking a handle", func(t *testing.T) {
		resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.IntegerType{}, Codec: "missing"})
		require.NoError(t, err)
		projection, err := rasql.NativeProjection(nativeCompositionStringDecoder{schema: resultSchema})
		require.NoError(t, err)
		base, err := rasql.Native(rasql.NativeStatement{Engine: "sqlite", SQL: "SELECT 1 AS value"}, projection, rasql.Many)
		require.NoError(t, err)
		source, err := rasql.Derive(base, "native_values")
		require.NoError(t, err)
		value, err := rasql.BindResultColumn[string, string](source, "value")
		require.NoError(t, err)
		outerSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.TextType{}, Codec: "missing"})
		require.NoError(t, err)
		outerProjection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("value", value.Expr(), schema.TextType{}, "missing")}, nativeCompositionStringDecoder{schema: outerSchema})
		require.NoError(t, err)
		query := rasql.Select(source.Source(), outerProjection)
		spy := &nativeCompositionSpyExecutor{dialect: dialect.PostgreSQL()}
		profile, err := rasql.EngineProfileFromVersion("postgresql-17", 17, 6, 0)
		require.NoError(t, err)
		executor, err := rasql.WithEngineProfile(spy, profile)
		require.NoError(t, err)
		registry := &nativeCompositionSpyRegistry{}
		executor, err = rasql.WithCodecs(executor, registry)
		require.NoError(t, err)
		_, err = rasql.All(t.Context(), executor, query)
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "engine_mismatch", planErr.Code)
		require.Equal(t, int64(0), registry.calls.Load())
		require.Equal(t, int64(0), spy.calls.Load())
	})

	t.Run("a missing argument codec precedes taking a handle", func(t *testing.T) {
		resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.IntegerType{}})
		require.NoError(t, err)
		projection, err := rasql.NativeProjection(nativeCompositionIntDecoder{schema: resultSchema})
		require.NoError(t, err)
		base, err := rasql.Native(rasql.NativeStatement{
			Engine: "sqlite",
			SQL:    "SELECT ? AS value",
			Args:   []rasql.NativeArgument{{Value: int64(1), Codec: "missing"}},
		}, projection, rasql.Many)
		require.NoError(t, err)
		source, err := rasql.Derive(base, "native_values")
		require.NoError(t, err)
		value, err := rasql.BindResultColumn[int64, int64](source, "value")
		require.NoError(t, err)
		outerProjection, err := rasql.Scalar("value", value.Expr(), schema.IntegerType{}, "")
		require.NoError(t, err)
		query := rasql.Select(source.Source(), outerProjection)
		spy := &nativeCompositionSpyExecutor{dialect: dialect.SQLite()}
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.WithEngineProfile(spy, profile)
		require.NoError(t, err)
		registry := &nativeCompositionSpyRegistry{}
		executor, err = rasql.WithCodecs(executor, registry)
		require.NoError(t, err)
		_, err = rasql.All(t.Context(), executor, query)
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "codec_unavailable", planErr.Code)
		require.Greater(t, registry.calls.Load(), int64(0))
		require.Equal(t, int64(0), spy.calls.Load())
	})

	t.Run("preserves nullable result metadata and decoding", func(t *testing.T) {
		resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{
			Name: "value", Type: schema.IntegerType{}, Nullable: true,
		})
		require.NoError(t, err)
		projection, err := rasql.NativeProjection(nativeCompositionNullableDecoder{schema: resultSchema})
		require.NoError(t, err)
		base, err := rasql.Native(rasql.NativeStatement{Engine: "sqlite", SQL: "SELECT NULL AS value"}, projection, rasql.Many)
		require.NoError(t, err)
		source, err := rasql.Derive(base, "native_values")
		require.NoError(t, err)
		value, err := rasql.BindNullResultColumn[nativeCompositionNullableRow, int64](source, "value")
		require.NoError(t, err)
		outerSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.IntegerType{}, Nullable: true})
		require.NoError(t, err)
		outerProjection, err := rasql.NewProjection([]rasql.ProjectionItem{
			rasql.NullItem("value", value.NullExpr(), schema.IntegerType{}, ""),
		}, nativeCompositionNullableDecoder{schema: outerSchema})
		require.NoError(t, err)
		executor := nativeCompositionSQLiteExecutor(t, "unused", &nativeCompositionCodec{})
		rows, err := rasql.All(t.Context(), executor, rasql.Select(source.Source(), outerProjection))
		require.NoError(t, err)
		require.Equal(t, []nativeCompositionNullableRow{{}}, rows)
	})

	t.Run("a derived table and a CTE relocate repeated placeholders on PostgreSQL", func(t *testing.T) {
		database := dbtest.PostgreSQLDB(t)
		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		profile, err := rasql.DiscoverEngineProfile(t.Context(), db, "postgresql-17")
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		innerSchema, err := rasql.NewResultSchema(
			rasql.ResultColumn{Name: "first", Type: schema.IntegerType{}},
			rasql.ResultColumn{Name: "repeated", Type: schema.IntegerType{}},
			rasql.ResultColumn{Name: "second", Type: schema.IntegerType{}},
			rasql.ResultColumn{Name: "third", Type: schema.IntegerType{}},
		)
		require.NoError(t, err)
		innerProjection, err := rasql.NativeProjection(nativeCompositionPGDecoder{schema: innerSchema})
		require.NoError(t, err)
		base, err := rasql.Native(rasql.NativeStatement{
			Engine: "postgresql",
			SQL:    "SELECT $2::bigint AS first, $2::bigint AS repeated, $1::bigint AS second, $3::bigint AS third",
			Args:   []rasql.NativeArgument{{Value: int64(11)}, {Value: int64(22)}, {Value: int64(33)}},
		}, innerProjection, rasql.Many)
		require.NoError(t, err)

		derived, err := rasql.Derive(base, "native_values")
		require.NoError(t, err)
		first, err := rasql.BindResultColumn[nativeCompositionPGRow, int64](derived, "first")
		require.NoError(t, err)
		whereValue := rasql.Value(int64(22))
		outerSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "first", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "marker", Type: schema.TextType{}})
		require.NoError(t, err)
		outerProjection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("first", first.Expr(), schema.IntegerType{}, ""), rasql.Item("marker", rasql.Value("99"), schema.TextType{}, "")}, nativeCompositionPGOuterDecoder{schema: outerSchema})
		require.NoError(t, err)
		derivedQuery := rasql.Select(derived.Source(), outerProjection).Where(rasql.EqualExpr(first.Expr(), whereValue))
		values, err := rasql.All(t.Context(), executor, derivedQuery)
		require.NoError(t, err)
		require.Equal(t, []nativeCompositionPGOuter{{First: 22, Marker: "99"}}, values)

		cte, err := rasql.CTEOf("native_values", base)
		require.NoError(t, err)
		cteSource, err := cte.Source("native_alias")
		require.NoError(t, err)
		cteFirst, err := rasql.BindResultColumn[nativeCompositionPGRow, int64](cteSource, "first")
		require.NoError(t, err)
		cteProjection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("first", cteFirst.Expr(), schema.IntegerType{}, ""), rasql.Item("marker", rasql.Value("99"), schema.TextType{}, "")}, nativeCompositionPGOuterDecoder{schema: outerSchema})
		require.NoError(t, err)
		cteQuery, err := rasql.With(rasql.Select(cteSource.Source(), cteProjection).Where(rasql.EqualExpr(cteFirst.Expr(), rasql.Value(int64(22)))), cte)
		require.NoError(t, err)
		values, err = rasql.All(t.Context(), executor, cteQuery)
		require.NoError(t, err)
		require.Equal(t, []nativeCompositionPGOuter{{First: 22, Marker: "99"}}, values)
	})
}

type nativeCompositionSnapshotValue struct {
	Value string
	Calls *atomic.Int64
}

func (value *nativeCompositionSnapshotValue) SnapshotBind() (*nativeCompositionSnapshotValue, error) {
	value.Calls.Add(1)
	return &nativeCompositionSnapshotValue{Value: value.Value, Calls: value.Calls}, nil
}

type nativeCompositionStringDecoder struct{ schema rasql.ResultSchema }

func (d nativeCompositionStringDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (nativeCompositionStringDecoder) Presence() []rasql.Presence         { return nil }
func (d nativeCompositionStringDecoder) DecodeRow(source rasql.ScanSource, value *string) error {
	return source.Scan(value)
}

type nativeCompositionSnapshotCodec struct{}

func (nativeCompositionSnapshotCodec) Encode(value any) (driver.Value, error) {
	return value.(*nativeCompositionSnapshotValue).Value, nil
}
func (nativeCompositionSnapshotCodec) Decode(value any, destination any) error {
	*destination.(*string) = value.(string)
	return nil
}

func projectionFromSnapshotSource(t *testing.T, source rasql.TypedSource[string]) rasql.Projection[string] {
	t.Helper()
	value, err := rasql.BindResultColumn[string, string](source, "value")
	require.NoError(t, err)
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.TextType{}, Codec: "snapshot"})
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("value", value.Expr(), schema.TextType{}, "snapshot")}, nativeCompositionStringDecoder{schema: resultSchema})
	require.NoError(t, err)
	return projection
}

type nativeCompositionSpyExecutor struct {
	calls   atomic.Int64
	dialect dialect.Dialect
}

func (e *nativeCompositionSpyExecutor) Dialect() dialect.Dialect { return e.dialect }
func (e *nativeCompositionSpyExecutor) Query(context.Context, stmt.Statement) (rasql.ResultRows, error) {
	e.calls.Add(1)
	return nil, errors.New("unexpected query call")
}
func (e *nativeCompositionSpyExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	e.calls.Add(1)
	return driver.RowsAffected(0), nil
}

type nativeCompositionSpyRegistry struct {
	calls atomic.Int64
}

func (r *nativeCompositionSpyRegistry) Lookup(rasql.CodecID) (rasql.ValueCodec, bool) {
	r.calls.Add(1)
	return nil, false
}

type nativeCompositionLifecycleRows struct {
	values       [][]any
	columns      []string
	index        int
	scanErr      error
	iterationErr error
	finishErr    error
	finishCalls  int
	closeCalls   int
	recorded     int
	primary      error
	early        bool
}

func (r *nativeCompositionLifecycleRows) Columns() ([]string, error) {
	return append([]string(nil), r.columns...), nil
}

func (r *nativeCompositionLifecycleRows) Next() bool {
	return r.index < len(r.values)
}

func (r *nativeCompositionLifecycleRows) Scan(destinations ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	if r.index >= len(r.values) {
		return errors.New("scan past end")
	}
	values := r.values[r.index]
	if len(values) != len(destinations) {
		return errors.New("lifecycle row column mismatch")
	}
	for i, destination := range destinations {
		value, ok := destination.(*any)
		if !ok {
			return errors.New("lifecycle row has unsupported destination")
		}
		*value = values[i]
	}
	r.index++
	return nil
}

func (r *nativeCompositionLifecycleRows) Close() error {
	r.closeCalls++
	return nil
}

func (r *nativeCompositionLifecycleRows) Err() error { return r.iterationErr }

func (r *nativeCompositionLifecycleRows) RecordRow() { r.recorded++ }

func (r *nativeCompositionLifecycleRows) Finish(primary error, early bool) error {
	r.finishCalls++
	r.primary = primary
	r.early = early
	if r.closeCalls == 0 {
		_ = r.Close()
	}
	return errors.Join(primary, r.finishErr)
}

type nativeCompositionLifecycleExecutor struct {
	dialect  dialect.Dialect
	rows     *nativeCompositionLifecycleRows
	queryErr error
	calls    atomic.Int64
}

func (e *nativeCompositionLifecycleExecutor) Dialect() dialect.Dialect { return e.dialect }
func (e *nativeCompositionLifecycleExecutor) Query(context.Context, stmt.Statement) (rasql.ResultRows, error) {
	e.calls.Add(1)
	if e.queryErr != nil {
		return nil, e.queryErr
	}
	return e.rows, nil
}
func (e *nativeCompositionLifecycleExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	return driver.RowsAffected(0), nil
}

func nativeCompositionLifecycleQuery(t *testing.T, cte bool) rasql.Query[int64] {
	t.Helper()
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.IntegerType{}})
	require.NoError(t, err)
	projection, err := rasql.NativeProjection(nativeCompositionIntDecoder{schema: resultSchema})
	require.NoError(t, err)
	base, err := rasql.Native(rasql.NativeStatement{Engine: "sqlite", SQL: "SELECT 1 AS value"}, projection, rasql.Many)
	require.NoError(t, err)
	var source rasql.TypedSource[int64]
	if cte {
		common, commonErr := rasql.CTEOf("native_values", base)
		require.NoError(t, commonErr)
		source, err = common.Source("native_alias")
		require.NoError(t, err)
		value, valueErr := rasql.BindResultColumn[int64, int64](source, "value")
		require.NoError(t, valueErr)
		outer, outerErr := rasql.Scalar("value", value.Expr(), schema.IntegerType{}, "")
		require.NoError(t, outerErr)
		query, queryErr := rasql.With(rasql.Select(source.Source(), outer), common)
		require.NoError(t, queryErr)
		return query
	}
	source, err = rasql.Derive(base, "native_values")
	require.NoError(t, err)
	value, err := rasql.BindResultColumn[int64, int64](source, "value")
	require.NoError(t, err)
	outer, err := rasql.Scalar("value", value.Expr(), schema.IntegerType{}, "")
	require.NoError(t, err)
	return rasql.Select(source.Source(), outer)
}

type nativeCompositionPGOuter struct {
	First  int64
	Marker string
}

type nativeCompositionIntDecoder struct{ schema rasql.ResultSchema }

func (d nativeCompositionIntDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (nativeCompositionIntDecoder) Presence() []rasql.Presence         { return nil }
func (d nativeCompositionIntDecoder) DecodeRow(source rasql.ScanSource, value *int64) error {
	return source.Scan(value)
}

type nativeCompositionNullableRow struct {
	Value rasql.Nullable[int64]
}

type nativeCompositionNullableDecoder struct{ schema rasql.ResultSchema }

func (d nativeCompositionNullableDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (nativeCompositionNullableDecoder) Presence() []rasql.Presence         { return nil }
func (d nativeCompositionNullableDecoder) DecodeRow(source rasql.ScanSource, value *nativeCompositionNullableRow) error {
	return source.Scan(&value.Value)
}

type nativeCompositionPGOuterDecoder struct{ schema rasql.ResultSchema }

func (d nativeCompositionPGOuterDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (nativeCompositionPGOuterDecoder) Presence() []rasql.Presence         { return nil }
func (d nativeCompositionPGOuterDecoder) DecodeRow(source rasql.ScanSource, value *nativeCompositionPGOuter) error {
	return source.Scan(&value.First, &value.Marker)
}

type nativeCompositionPGRow struct {
	First    int64
	Repeated int64
	Second   int64
	Third    int64
}

type nativeCompositionPGDecoder struct{ schema rasql.ResultSchema }

func (d nativeCompositionPGDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (nativeCompositionPGDecoder) Presence() []rasql.Presence         { return nil }
func (d nativeCompositionPGDecoder) DecodeRow(source rasql.ScanSource, value *nativeCompositionPGRow) error {
	return source.Scan(&value.First, &value.Repeated, &value.Second, &value.Third)
}

type nativeCompositionOuterDecoder struct{ schema rasql.ResultSchema }

func (d nativeCompositionOuterDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (nativeCompositionOuterDecoder) Presence() []rasql.Presence         { return nil }
func (d nativeCompositionOuterDecoder) DecodeRow(source rasql.ScanSource, value *nativeCompositionOuter) error {
	return source.Scan(&value.First, &value.Marker)
}
