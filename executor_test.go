package rasql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

type runtimeFakeRows struct {
	values                                   [][]any
	columns                                  []string
	index                                    int
	recorded                                 int
	finished                                 int
	closed                                   int
	columnsErr, iterErr, finishErr, closeErr error
	lastFinish                               error
	lastEarly                                bool
}

func (r *runtimeFakeRows) Columns() ([]string, error) {
	if r.columnsErr != nil {
		return nil, r.columnsErr
	}
	if r.columns != nil {
		return append([]string(nil), r.columns...), nil
	}
	return []string{"value"}, nil
}
func (r *runtimeFakeRows) Next() bool { return r.index < len(r.values) }
func (r *runtimeFakeRows) Scan(destinations ...any) error {
	if r.index >= len(r.values) {
		return sql.ErrNoRows
	}
	for i, destination := range destinations {
		if i >= len(r.values[r.index]) {
			return sql.ErrNoRows
		}
		setRuntimeDestination(destination, r.values[r.index][i])
	}
	r.index++
	return nil
}
func (r *runtimeFakeRows) Close() error { r.closed++; return r.closeErr }
func (r *runtimeFakeRows) Err() error   { return r.iterErr }
func (r *runtimeFakeRows) RecordRow()   { r.recorded++ }

func (r *runtimeFakeRows) Finish(primary error, early bool) error {
	r.finished++
	r.lastFinish, r.lastEarly = primary, early
	return errors.Join(primary, r.finishErr)
}
func setRuntimeDestination(destination, value any) {
	v := reflect.ValueOf(destination)
	if value == nil {
		v.Elem().SetZero()
		return
	}
	if v.Elem().Kind() == reflect.Interface {
		v.Elem().Set(reflect.ValueOf(value))
		return
	}
	if scanner, ok := destination.(sql.Scanner); ok {
		_ = scanner.Scan(value)
		return
	}
	source := reflect.ValueOf(value)
	if !source.IsValid() {
		v.Elem().SetZero()
		return
	}
	if source.Type().AssignableTo(v.Elem().Type()) {
		v.Elem().Set(source)
		return
	}
	if source.Type().ConvertibleTo(v.Elem().Type()) {
		v.Elem().Set(source.Convert(v.Elem().Type()))
	}
}

type runtimeFakeExecutor struct {
	calls         atomic.Int64
	rows          [][]any
	dialect       dialect.Dialect
	last          *runtimeFakeRows
	mu            sync.Mutex
	lastStatement stmt.Statement
}
type nilMapExecutor map[string]string

func (nilMapExecutor) Dialect() dialect.Dialect { panic("typed nil executor called Dialect") }
func (nilMapExecutor) Query(context.Context, stmt.Statement) (ResultRows, error) {
	panic("typed nil executor called Query")
}
func (nilMapExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	panic("typed nil executor called Exec")
}

func (e *runtimeFakeExecutor) Dialect() dialect.Dialect { return e.dialect }
func (e *runtimeFakeExecutor) Query(_ context.Context, statement stmt.Statement) (ResultRows, error) {
	e.calls.Add(1)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastStatement = statement
	e.last = &runtimeFakeRows{values: e.rows}
	return e.last, nil
}
func (*runtimeFakeExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	return driver.RowsAffected(0), nil
}

type runtimeDecoder struct{ schema ResultSchema }

func (d runtimeDecoder) ResultSchema() ResultSchema                     { return d.schema }
func (runtimeDecoder) Presence() []Presence                             { return nil }
func (runtimeDecoder) DecodeRow(source ScanSource, result *int64) error { return source.Scan(result) }

type runtimeBytesDecoder struct{ schema ResultSchema }

func (d runtimeBytesDecoder) ResultSchema() ResultSchema { return d.schema }
func (runtimeBytesDecoder) Presence() []Presence         { return nil }
func (runtimeBytesDecoder) DecodeRow(source ScanSource, result *[]byte) error {
	return source.Scan(result)
}

type runtimePair struct{ First, Second int64 }
type runtimePairDecoder struct{ schema ResultSchema }

func (d runtimePairDecoder) ResultSchema() ResultSchema { return d.schema }
func (runtimePairDecoder) Presence() []Presence         { return nil }
func (runtimePairDecoder) DecodeRow(source ScanSource, result *runtimePair) error {
	return source.Scan(&result.First, &result.Second)
}

func runtimeQuery(t *testing.T) Query[int64] {
	t.Helper()
	table, err := ReadTableOf[struct{}](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "value", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	relation, err := SourceOf(table, "i")
	require.NoError(t, err)
	column, err := BindColumn[struct{}, int64](relation, "value", "")
	require.NoError(t, err)
	resultSchema, err := NewResultSchema(ResultColumn{Name: "value", Type: schema.IntegerType{}})
	require.NoError(t, err)
	projection, err := NewProjection([]ProjectionItem{Item("value", column.Expr(), schema.IntegerType{}, "")}, runtimeDecoder{schema: resultSchema})
	require.NoError(t, err)
	return Select(relation.Source(), projection)
}
func runtimePairQuery(t *testing.T, firstCodec, secondCodec string) Query[runtimePair] {
	t.Helper()
	table, err := ReadTableOf[runtimePair](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "first", Type: schema.IntegerType{}}, {Name: "second", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	relation, err := SourceOf(table, "i")
	require.NoError(t, err)
	first, err := BindColumn[runtimePair, int64](relation, "first", firstCodec)
	require.NoError(t, err)
	second, err := BindColumn[runtimePair, int64](relation, "second", secondCodec)
	require.NoError(t, err)
	schemaValue, err := NewResultSchema(ResultColumn{Name: "first", Type: schema.IntegerType{}, Codec: firstCodec}, ResultColumn{Name: "second", Type: schema.IntegerType{}, Codec: secondCodec})
	require.NoError(t, err)
	projection, err := NewProjection([]ProjectionItem{Item("first", first.Expr(), schema.IntegerType{}, firstCodec), Item("second", second.Expr(), schema.IntegerType{}, secondCodec)}, runtimePairDecoder{schema: schemaValue})
	require.NoError(t, err)
	return Select(relation.Source(), projection)
}

func runtimeExecutor(t *testing.T, rows [][]any) Executor {
	t.Helper()
	raw := &runtimeFakeExecutor{rows: rows, dialect: dialect.SQLite()}
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := WithEngineProfile(raw, profile)
	require.NoError(t, err)
	return executor
}

func TestPreparedRowsCardinalityAndEarlyStop(t *testing.T) {
	q := runtimeQuery(t)
	for name, values := range map[string][][]any{"zero": {}, "one": {{int64(7)}}, "two": {{int64(7)}, {int64(8)}}} {
		t.Run(name, func(t *testing.T) {
			executor := runtimeExecutor(t, values)
			all, err := All(t.Context(), executor, q)
			require.NoError(t, err)
			require.Len(t, all, len(values))
		})
	}
	zeroExec := runtimeExecutor(t, nil)
	_, err := One(t.Context(), zeroExec, q)
	require.ErrorIs(t, err, ErrNoRows)
	zeroRaw := zeroExec.(profiledExecutor).Executor.(*runtimeFakeExecutor)
	require.ErrorIs(t, zeroRaw.last.lastFinish, ErrNoRows)
	twoExec := runtimeExecutor(t, [][]any{{int64(1)}, {int64(2)}})
	_, err = One(t.Context(), twoExec, q)
	require.ErrorIs(t, err, ErrMultipleRows)
	twoRaw := twoExec.(profiledExecutor).Executor.(*runtimeFakeExecutor)
	require.ErrorIs(t, twoRaw.last.lastFinish, ErrMultipleRows)
	maybeZero := runtimeExecutor(t, nil)
	_, found, err := Maybe(t.Context(), maybeZero, q)
	require.NoError(t, err)
	require.False(t, found)
	maybeTwo := runtimeExecutor(t, [][]any{{int64(1)}, {int64(2)}})
	_, _, err = Maybe(t.Context(), maybeTwo, q)
	require.ErrorIs(t, err, ErrMultipleRows)
	executor := runtimeExecutor(t, [][]any{{int64(1)}, {int64(2)}})
	rows, err := Rows(t.Context(), executor, q)
	require.NoError(t, err)
	count := 0
	for range rows {
		count++
		break
	}
	require.Equal(t, 1, count)
}

func TestPreparedRowsChecksReturnedColumnsAndJoinsFinish(t *testing.T) {
	schemaValue := mustRuntimeSchema(t, ResultColumn{Name: "value", Type: schema.IntegerType{}})
	prepared := preparedRows[int64]{statement: stmt.New(sqltext.Text("SELECT value")), schema: schemaValue, decoder: runtimeDecoder{schema: schemaValue}, codecs: builtinCodecs}
	for name, columns := range map[string][]string{"reordered": {"other"}, "extra": {"value", "other"}, "duplicate": {"value", "value"}} {
		t.Run(name, func(t *testing.T) {
			rows := &runtimeFakeRows{columns: columns, values: [][]any{{int64(1)}}, finishErr: errors.New("finish")}
			sequence, err := rowsPrepared(context.Background(), runtimeRowsExecutor{rows: rows}, prepared)
			require.NoError(t, err)
			var got error
			for _, err := range sequence {
				got = err
				break
			}
			require.Error(t, got)
			require.ErrorContains(t, got, "finish")
			require.Equal(t, 0, rows.recorded)
			require.Equal(t, 1, rows.finished)
		})
	}
	rows := &runtimeFakeRows{columnsErr: errors.New("columns"), finishErr: errors.New("finish")}
	sequence, err := rowsPrepared(context.Background(), runtimeRowsExecutor{rows: rows}, prepared)
	require.NoError(t, err)
	var got error
	for _, err := range sequence {
		got = err
		break
	}
	require.Error(t, got)
	require.ErrorContains(t, got, "columns")
	require.ErrorContains(t, got, "finish")
	require.Equal(t, 1, rows.finished)
}

func TestRowsConcurrentQueryReuse(t *testing.T) {
	q := runtimeQuery(t)
	raw := &runtimeFakeExecutor{rows: [][]any{{int64(1)}}, dialect: dialect.SQLite()}
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := WithEngineProfile(raw, profile)
	require.NoError(t, err)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			values, err := All(context.Background(), executor, q)
			require.NoError(t, err)
			require.Equal(t, []int64{1}, values)
		}()
	}
	wg.Wait()
	require.Equal(t, int64(100), raw.calls.Load())
}

func TestTypedNilExecutorRejectedBeforeCapabilities(t *testing.T) {
	var executor nilMapExecutor
	registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"x": testCodec{}})
	require.NoError(t, err)
	_, err = WithCodecs(executor, registry)
	require.ErrorContains(t, err, "executor must not be nil")
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	_, err = WithEngineProfile(executor, profile)
	require.ErrorContains(t, err, "executor must not be nil")
}

func TestResultRowsDoNotAliasByteValues(t *testing.T) {
	schemaValue := mustRuntimeSchema(t, ResultColumn{Name: "value", Type: schema.BytesType{}})
	values := make([][]any, 100)
	for i := range values {
		values[i] = []any{[]byte{byte(i)}}
	}
	prepared := preparedRows[[]byte]{statement: stmt.New(sqltext.Text("SELECT value")), schema: schemaValue, decoder: runtimeBytesDecoder{schema: schemaValue}, codecs: builtinCodecs}
	sequence, err := rowsPrepared(context.Background(), runtimeRowsExecutor{rows: &runtimeFakeRows{values: values}}, prepared)
	require.NoError(t, err)
	var got [][]byte
	for value, err := range sequence {
		require.NoError(t, err)
		got = append(got, value)
	}
	require.Len(t, got, 100)
	got[0][0] = 99
	require.Equal(t, byte(1), got[1][0])
}

func TestPreparedRowsCodecCountsAndNullBypass(t *testing.T) {
	var enc, dec atomic.Int64
	codec := countingRuntimeCodec{encode: &enc, decode: &dec}
	registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"count": codec})
	require.NoError(t, err)
	statement := stmt.New(sqltext.Text("SELECT ?"), int64(1))
	prepared := preparedRows[int64]{statement: statement, schema: mustRuntimeSchema(t, ResultColumn{Name: "value", Type: schema.IntegerType{}, Codec: "count", Nullable: true}), decoder: runtimeDecoder{schema: mustRuntimeSchema(t, ResultColumn{Name: "value", Type: schema.IntegerType{}, Codec: "count", Nullable: true})}, codecs: registry}
	rows := &runtimeFakeRows{values: [][]any{{int64(3)}}}
	executor := runtimeRowsExecutor{rows: rows}
	sequence, err := rowsPrepared(context.Background(), executor, prepared)
	require.NoError(t, err)
	var values []int64
	for value, err := range sequence {
		require.NoError(t, err)
		values = append(values, value)
	}
	require.Equal(t, []int64{3}, values)
	require.Equal(t, int64(1), dec.Load())
	require.Equal(t, int64(0), enc.Load())
	var nullable sql.NullInt64
	nullSource := codecScanSource{source: &runtimeFakeRows{values: [][]any{{nil}}}, columns: prepared.schema.Columns(), codecs: []ValueCodec{codec}}
	require.NoError(t, nullSource.Scan(&nullable))
	require.False(t, nullable.Valid)
	require.Equal(t, int64(1), dec.Load())
}

func TestPreparedRowsCopiesAndEncodesOneDetachedStatement(t *testing.T) {
	var copies, encodes atomic.Int64
	var decodes atomic.Int64
	codec := countingRuntimeCodec{encode: &encodes, decode: &decodes}
	registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"count": codec})
	require.NoError(t, err)
	base := runtimeExecutor(t, nil)
	inner, err := WithCodecs(base, registry)
	require.NoError(t, err)
	compiled := compiledQuery{statement: stmt.New(sqltext.Text("SELECT ?"), sql.Named("payload", []byte("x"))), bindSlots: []bindSlot{{id: 1, codec: "count"}}, copyArgs: []bindValueCopy{func() (any, error) { copies.Add(1); return sql.Named("payload", []byte("x")), nil }}}
	prepared, err := prepareRows(inner, runtimeQuery(t), compiled)
	require.NoError(t, err)
	require.Equal(t, int64(1), copies.Load())
	require.Equal(t, int64(1), encodes.Load())
	sequence, err := rowsPrepared(context.Background(), inner, prepared)
	require.NoError(t, err)
	for _, rowErr := range sequence {
		require.NoError(t, rowErr)
	}
	raw := base.(profiledExecutor).Executor.(*runtimeFakeExecutor)
	require.Equal(t, int64(1), raw.calls.Load())
	require.Equal(t, "SELECT ?", raw.lastStatement.SQL())
	handoff := raw.lastStatement.Args()[0].(sql.NamedArg)
	require.Equal(t, "payload", handoff.Name)
	require.Equal(t, []byte("x"), handoff.Value)
	require.Equal(t, int64(1), copies.Load())
	require.Equal(t, int64(1), encodes.Load())
	handoff.Value.([]byte)[0] = 'z'
	require.Equal(t, []byte("x"), compiled.statement.Args()[0].(sql.NamedArg).Value)
}

func TestPreparedRowsCopyFailuresRunNoCodecOrExecutor(t *testing.T) {
	copyFailure := errors.New("copy failed")
	var encodes, decodes atomic.Int64
	codec := countingRuntimeCodec{encode: &encodes, decode: &decodes}
	registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"count": codec})
	require.NoError(t, err)
	base := runtimeExecutor(t, nil)
	inner, err := WithCodecs(base, registry)
	require.NoError(t, err)
	compiled := compiledQuery{statement: stmt.New(sqltext.Text("SELECT ?"), 1), bindSlots: []bindSlot{{id: 1, codec: "count"}}, copyArgs: []bindValueCopy{func() (any, error) { return nil, copyFailure }}}
	_, err = prepareRows(inner, runtimeQuery(t), compiled)
	require.ErrorIs(t, err, copyFailure)
	require.Equal(t, int64(0), encodes.Load())
	raw := base.(profiledExecutor).Executor.(*runtimeFakeExecutor)
	require.Equal(t, int64(0), raw.calls.Load())
	for name, broken := range map[string]compiledQuery{
		"missing slot":     {statement: stmt.New(sqltext.Text("SELECT ?"), 1), bindSlots: nil, copyArgs: []bindValueCopy{func() (any, error) { return 1, nil }}},
		"missing copier":   {statement: stmt.New(sqltext.Text("SELECT ?"), 1), bindSlots: []bindSlot{{id: 1, codec: "count"}}, copyArgs: nil},
		"missing argument": {statement: stmt.New(sqltext.Text("SELECT")), bindSlots: []bindSlot{{id: 1, codec: "count"}}, copyArgs: []bindValueCopy{func() (any, error) { return 1, nil }}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := prepareRows(inner, runtimeQuery(t), broken)
			require.Error(t, err)
			require.Equal(t, int64(0), encodes.Load())
			require.Equal(t, int64(0), raw.calls.Load())
		})
	}
}

func TestPreparedRowsMissingCodecPathsAreIndexed(t *testing.T) {
	var counter atomic.Int64
	registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"present": countingRuntimeCodec{encode: &counter, decode: &counter}})
	require.NoError(t, err)
	base := runtimeExecutor(t, nil)
	inner, err := WithCodecs(base, registry)
	require.NoError(t, err)
	compiled := compiledQuery{statement: stmt.New(sqltext.Text("SELECT ?"), 1), bindSlots: []bindSlot{{id: 1, codec: "present"}}, copyArgs: []bindValueCopy{func() (any, error) { return 1, nil }}}
	_, err = prepareRows(inner, runtimePairQuery(t, "present", "missing"), compiled)
	var planErr *PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "result.columns[1].codec", planErr.Path)
	_, err = prepareRows(inner, runtimeQuery(t), compiledQuery{statement: stmt.New(sqltext.Text("SELECT ?"), 1, 2), bindSlots: []bindSlot{{id: 1, codec: "present"}, {id: 2, codec: "missing"}}, copyArgs: []bindValueCopy{func() (any, error) { return 1, nil }, func() (any, error) { return 2, nil }}})
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "binds[1].codec", planErr.Path)
	require.Equal(t, int64(0), base.(profiledExecutor).Executor.(*runtimeFakeExecutor).calls.Load())
}

type countingRuntimeCodec struct{ encode, decode *atomic.Int64 }

func (c countingRuntimeCodec) Encode(v any) (driver.Value, error) { c.encode.Add(1); return v, nil }
func (c countingRuntimeCodec) Decode(v any, d any) error {
	c.decode.Add(1)
	*d.(*int64) = v.(int64)
	return nil
}

type runtimeRowsExecutor struct{ rows ResultRows }

func (e runtimeRowsExecutor) Dialect() dialect.Dialect { return dialect.SQLite() }
func (e runtimeRowsExecutor) Query(context.Context, stmt.Statement) (ResultRows, error) {
	return e.rows, nil
}
func (runtimeRowsExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	return driver.RowsAffected(0), nil
}
func mustRuntimeSchema(t *testing.T, column ResultColumn) ResultSchema {
	s, err := NewResultSchema(column)
	require.NoError(t, err)
	return s
}
