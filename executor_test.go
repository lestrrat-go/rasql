package rasql_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/bindplan"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
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
	if r.closed == 0 {
		_ = r.Close()
	}
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
func (e *runtimeFakeExecutor) Dialect() dialect.Dialect { return e.dialect }
func (e *runtimeFakeExecutor) Query(_ context.Context, statement stmt.Statement) (rasql.ResultRows, error) {
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

type runtimeDecoder struct{ schema rasql.ResultSchema }

func (d runtimeDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (runtimeDecoder) Presence() []rasql.Presence         { return nil }
func (runtimeDecoder) DecodeRow(source rasql.ScanSource, result *int64) error {
	return source.Scan(result)
}

type runtimeBytesDecoder struct{ schema rasql.ResultSchema }

func (d runtimeBytesDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (runtimeBytesDecoder) Presence() []rasql.Presence         { return nil }
func (runtimeBytesDecoder) DecodeRow(source rasql.ScanSource, result *[]byte) error {
	return source.Scan(result)
}

type runtimePair struct{ First, Second int64 }
type runtimePairDecoder struct{ schema rasql.ResultSchema }

func (d runtimePairDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (runtimePairDecoder) Presence() []rasql.Presence         { return nil }
func (runtimePairDecoder) DecodeRow(source rasql.ScanSource, result *runtimePair) error {
	return source.Scan(&result.First, &result.Second)
}

func runtimeQuery(t *testing.T) rasql.Query[int64] {
	t.Helper()
	table, err := rasql.ReadTableOf[struct{}](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "value", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "i")
	require.NoError(t, err)
	column, err := rasql.BindColumn[struct{}, int64](relation, "value", "")
	require.NoError(t, err)
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.IntegerType{}})
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("value", column.Expr(), schema.IntegerType{}, "")}, runtimeDecoder{schema: resultSchema})
	require.NoError(t, err)
	return rasql.Select(relation.Source(), projection)
}

// runtimeBytesQuery projects one bytes column, so a caller can check that two
// rows never share a buffer.
func runtimeBytesQuery(t *testing.T) rasql.Query[[]byte] {
	t.Helper()
	table, err := rasql.ReadTableOf[struct{}](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "value", Type: schema.BytesType{}}}})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "i")
	require.NoError(t, err)
	column, err := rasql.BindColumn[struct{}, []byte](relation, "value", "")
	require.NoError(t, err)
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.BytesType{}})
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("value", column.Expr(), schema.BytesType{}, "")}, runtimeBytesDecoder{schema: resultSchema})
	require.NoError(t, err)
	return rasql.Select(relation.Source(), projection)
}

// runtimeRowsProfiled returns an executor that answers every query with rows,
// carrying the engine profile the row pipeline needs.
func runtimeRowsProfiled(t *testing.T, rows *runtimeFakeRows) rasql.Executor {
	t.Helper()
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.WithEngineProfile(runtimeRowsExecutor{rows: rows}, profile)
	require.NoError(t, err)
	return executor
}

// runtimeCodecQuery projects one nullable column through a named codec, so a
// caller can watch what the codec is asked to decode.
func runtimeCodecQuery(t *testing.T, codec string) rasql.Query[int64] {
	t.Helper()
	table, err := rasql.ReadTableOf[struct{}](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "value", Type: schema.IntegerType{}, Nullable: true}}})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "i")
	require.NoError(t, err)
	column, err := rasql.BindNullColumn[struct{}, int64](relation, "value", codec)
	require.NoError(t, err)
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.IntegerType{}, Codec: codec, Nullable: true})
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.NullItem("value", column.NullExpr(), schema.IntegerType{}, codec)}, runtimeDecoder{schema: resultSchema})
	require.NoError(t, err)
	return rasql.Select(relation.Source(), projection)
}

func runtimePairQuery(t *testing.T, firstCodec, secondCodec string) rasql.Query[runtimePair] {
	t.Helper()
	table, err := rasql.ReadTableOf[runtimePair](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "first", Type: schema.IntegerType{}}, {Name: "second", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "i")
	require.NoError(t, err)
	first, err := rasql.BindColumn[runtimePair, int64](relation, "first", firstCodec)
	require.NoError(t, err)
	second, err := rasql.BindColumn[runtimePair, int64](relation, "second", secondCodec)
	require.NoError(t, err)
	schemaValue, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "first", Type: schema.IntegerType{}, Codec: firstCodec}, rasql.ResultColumn{Name: "second", Type: schema.IntegerType{}, Codec: secondCodec})
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("first", first.Expr(), schema.IntegerType{}, firstCodec), rasql.Item("second", second.Expr(), schema.IntegerType{}, secondCodec)}, runtimePairDecoder{schema: schemaValue})
	require.NoError(t, err)
	return rasql.Select(relation.Source(), projection)
}

// runtimeExecutor returns an executor and the fake behind it, so a caller that
// needs to look at what the fake saw does not have to unwrap the executor.
func runtimeExecutor(t *testing.T, rows [][]any) (rasql.Executor, *runtimeFakeExecutor) {
	t.Helper()
	raw := &runtimeFakeExecutor{rows: rows, dialect: dialect.SQLite()}
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.WithEngineProfile(raw, profile)
	require.NoError(t, err)
	return executor, raw
}

func TestPreparedRows(t *testing.T) {
	t.Run("cardinality and early stop", func(t *testing.T) {
		q := runtimeQuery(t)
		for name, values := range map[string][][]any{"zero": {}, "one": {{int64(7)}}, "two": {{int64(7)}, {int64(8)}}} {
			t.Run(name, func(t *testing.T) {
				executor, _ := runtimeExecutor(t, values)
				all, err := rasql.All(t.Context(), executor, q)
				require.NoError(t, err)
				require.Len(t, all, len(values))
			})
		}
		zeroExec, zeroRaw := runtimeExecutor(t, nil)
		_, err := rasql.One(t.Context(), zeroExec, q)
		require.ErrorIs(t, err, rasql.ErrNoRows)
		require.ErrorIs(t, zeroRaw.last.lastFinish, rasql.ErrNoRows)
		twoExec, twoRaw := runtimeExecutor(t, [][]any{{int64(1)}, {int64(2)}})
		_, err = rasql.One(t.Context(), twoExec, q)
		require.ErrorIs(t, err, rasql.ErrMultipleRows)
		require.ErrorIs(t, twoRaw.last.lastFinish, rasql.ErrMultipleRows)
		maybeZero, _ := runtimeExecutor(t, nil)
		_, found, err := rasql.Maybe(t.Context(), maybeZero, q)
		require.NoError(t, err)
		require.False(t, found)
		maybeTwo, _ := runtimeExecutor(t, [][]any{{int64(1)}, {int64(2)}})
		_, _, err = rasql.Maybe(t.Context(), maybeTwo, q)
		require.ErrorIs(t, err, rasql.ErrMultipleRows)
		executor, _ := runtimeExecutor(t, [][]any{{int64(1)}, {int64(2)}})
		rows, err := rasql.Rows(t.Context(), executor, q)
		require.NoError(t, err)
		count := 0
		for range rows {
			count++
			break
		}
		require.Equal(t, 1, count)
	})

	t.Run("checks returned columns and joins finish", func(t *testing.T) {
		q := runtimeQuery(t)
		for name, columns := range map[string][]string{"reordered": {"other"}, "extra": {"value", "other"}, "duplicate": {"value", "value"}} {
			t.Run(name, func(t *testing.T) {
				rows := &runtimeFakeRows{columns: columns, values: [][]any{{int64(1)}}, finishErr: errors.New("finish")}
				sequence, err := rasql.Rows(t.Context(), runtimeRowsProfiled(t, rows), q)
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
		sequence, err := rasql.Rows(t.Context(), runtimeRowsProfiled(t, rows), q)
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
	})

	t.Run("codec counts and null bypass", func(t *testing.T) {
		var enc, dec atomic.Int64
		codec := countingRuntimeCodec{encode: &enc, decode: &dec}
		registry, err := rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{"count": codec})
		require.NoError(t, err)
		rows := &runtimeFakeRows{values: [][]any{{int64(3)}}}
		executor, err := rasql.WithCodecs(runtimeRowsProfiled(t, rows), registry)
		require.NoError(t, err)
		sequence, err := rasql.Rows(t.Context(), executor, runtimeCodecQuery(t, "count"))
		require.NoError(t, err)
		var values []int64
		for value, err := range sequence {
			require.NoError(t, err)
			values = append(values, value)
		}
		require.Equal(t, []int64{3}, values)
		require.Equal(t, int64(1), dec.Load())
		require.Equal(t, int64(0), enc.Load())
		// A null value bypassing the codec is covered by TestCodecScanSource.
	})

	t.Run("copies and encodes one detached statement", func(t *testing.T) {
		var copies, encodes atomic.Int64
		var decodes atomic.Int64
		codec := countingRuntimeCodec{encode: &encodes, decode: &decodes}
		registry, err := rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{"count": codec})
		require.NoError(t, err)
		base, raw := runtimeExecutor(t, nil)
		inner, err := rasql.WithCodecs(base, registry)
		require.NoError(t, err)
		compiled := bindplan.Compiled{Statement: stmt.New(sqltext.Text("SELECT ?"), sql.Named("payload", []byte("x"))), Slots: []bindplan.Slot{{ID: 1, Codec: "count"}}, CopyArgs: []bindplan.ValueCopy{func() (any, error) { copies.Add(1); return sql.Named("payload", []byte("x")), nil }}}
		prepared, err := rasql.Q1PrepareRows(inner, runtimeQuery(t), compiled)
		require.NoError(t, err)
		require.Equal(t, int64(1), copies.Load())
		require.Equal(t, int64(1), encodes.Load())
		sequence, err := rasql.Q1Rows(t.Context(), inner, prepared)
		require.NoError(t, err)
		for _, rowErr := range sequence {
			require.NoError(t, rowErr)
		}
		require.Equal(t, int64(1), raw.calls.Load())
		require.Equal(t, "SELECT ?", raw.lastStatement.SQL())
		handoff := raw.lastStatement.Args()[0].(sql.NamedArg)
		require.Equal(t, "payload", handoff.Name)
		require.Equal(t, []byte("x"), handoff.Value)
		require.Equal(t, int64(1), copies.Load())
		require.Equal(t, int64(1), encodes.Load())
		handoff.Value.([]byte)[0] = 'z'
		require.Equal(t, []byte("x"), compiled.Statement.Args()[0].(sql.NamedArg).Value)
	})

	t.Run("a copy failure runs neither codec nor executor", func(t *testing.T) {
		copyFailure := errors.New("copy failed")
		var encodes, decodes atomic.Int64
		codec := countingRuntimeCodec{encode: &encodes, decode: &decodes}
		registry, err := rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{"count": codec})
		require.NoError(t, err)
		base, raw := runtimeExecutor(t, nil)
		inner, err := rasql.WithCodecs(base, registry)
		require.NoError(t, err)
		compiled := bindplan.Compiled{Statement: stmt.New(sqltext.Text("SELECT ?"), 1), Slots: []bindplan.Slot{{ID: 1, Codec: "count"}}, CopyArgs: []bindplan.ValueCopy{func() (any, error) { return nil, copyFailure }}}
		_, err = rasql.Q1PrepareRows(inner, runtimeQuery(t), compiled)
		require.ErrorIs(t, err, copyFailure)
		require.Equal(t, int64(0), encodes.Load())
		require.Equal(t, int64(0), raw.calls.Load())
		for name, broken := range map[string]bindplan.Compiled{
			"missing slot":     {Statement: stmt.New(sqltext.Text("SELECT ?"), 1), Slots: nil, CopyArgs: []bindplan.ValueCopy{func() (any, error) { return 1, nil }}},
			"missing copier":   {Statement: stmt.New(sqltext.Text("SELECT ?"), 1), Slots: []bindplan.Slot{{ID: 1, Codec: "count"}}, CopyArgs: nil},
			"missing argument": {Statement: stmt.New(sqltext.Text("SELECT")), Slots: []bindplan.Slot{{ID: 1, Codec: "count"}}, CopyArgs: []bindplan.ValueCopy{func() (any, error) { return 1, nil }}},
		} {
			t.Run(name, func(t *testing.T) {
				_, err := rasql.Q1PrepareRows(inner, runtimeQuery(t), broken)
				require.Error(t, err)
				require.Equal(t, int64(0), encodes.Load())
				require.Equal(t, int64(0), raw.calls.Load())
			})
		}
	})

	t.Run("missing codec paths are indexed", func(t *testing.T) {
		var counter atomic.Int64
		registry, err := rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{"present": countingRuntimeCodec{encode: &counter, decode: &counter}})
		require.NoError(t, err)
		base, raw := runtimeExecutor(t, nil)
		inner, err := rasql.WithCodecs(base, registry)
		require.NoError(t, err)
		compiled := bindplan.Compiled{Statement: stmt.New(sqltext.Text("SELECT ?"), 1), Slots: []bindplan.Slot{{ID: 1, Codec: "present"}}, CopyArgs: []bindplan.ValueCopy{func() (any, error) { return 1, nil }}}
		_, err = rasql.Q1PrepareRows(inner, runtimePairQuery(t, "present", "missing"), compiled)
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "result.columns[1].codec", planErr.Path)
		_, err = rasql.Q1PrepareRows(inner, runtimeQuery(t), bindplan.Compiled{Statement: stmt.New(sqltext.Text("SELECT ?"), 1, 2), Slots: []bindplan.Slot{{ID: 1, Codec: "present"}, {ID: 2, Codec: "missing"}}, CopyArgs: []bindplan.ValueCopy{func() (any, error) { return 1, nil }, func() (any, error) { return 2, nil }}})
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "binds[1].codec", planErr.Path)
		require.Equal(t, int64(0), raw.calls.Load())
	})
}

func TestResultRows(t *testing.T) {
	t.Run("a query is reusable concurrently", func(t *testing.T) {
		q := runtimeQuery(t)
		raw := &runtimeFakeExecutor{rows: [][]any{{int64(1)}}, dialect: dialect.SQLite()}
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.WithEngineProfile(raw, profile)
		require.NoError(t, err)
		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				values, err := rasql.All(context.Background(), executor, q)
				require.NoError(t, err)
				require.Equal(t, []int64{1}, values)
			}()
		}
		wg.Wait()
		require.Equal(t, int64(100), raw.calls.Load())
	})

	t.Run("byte values are never aliased", func(t *testing.T) {
		values := make([][]any, 100)
		for i := range values {
			values[i] = []any{[]byte{byte(i)}}
		}
		rows := &runtimeFakeRows{values: values}
		sequence, err := rasql.Rows(t.Context(), runtimeRowsProfiled(t, rows), runtimeBytesQuery(t))
		require.NoError(t, err)
		var got [][]byte
		for value, err := range sequence {
			require.NoError(t, err)
			got = append(got, value)
		}
		require.Len(t, got, 100)
		got[0][0] = 99
		require.Equal(t, byte(1), got[1][0])
	})
}

func TestExecutor(t *testing.T) {
	t.Run("SQLite scans rows and projections directly", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { _ = database.Close() })
		_, err = database.Exec(`CREATE TABLE items (id INTEGER NOT NULL, name TEXT, payload BLOB)`)
		require.NoError(t, err)
		for i := 1; i <= 100; i++ {
			var name any = "name"
			var payload any = []byte{byte(i)}
			if i == 50 {
				name = nil
			}
			if i == 75 {
				payload = nil
			}
			_, err = database.Exec(`INSERT INTO items VALUES (?,?,?)`, i, name, payload)
			require.NoError(t, err)
		}
		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		table, err := rasql.ReadTableOf[sqliteRuntimeRow](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.TextType{}, Nullable: true}, {Name: "payload", Type: schema.BytesType{}, Nullable: true}}})
		require.NoError(t, err)
		relation, err := rasql.SourceOf(table, "i")
		require.NoError(t, err)
		id, err := rasql.BindColumn[sqliteRuntimeRow, int64](relation, "id", "")
		require.NoError(t, err)
		name, err := rasql.BindNullColumn[sqliteRuntimeRow, string](relation, "name", "")
		require.NoError(t, err)
		payload, err := rasql.BindNullColumn[sqliteRuntimeRow, []byte](relation, "payload", "")
		require.NoError(t, err)
		resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "name", Type: schema.TextType{}, Nullable: true}, rasql.ResultColumn{Name: "payload", Type: schema.BytesType{}, Nullable: true})
		require.NoError(t, err)
		projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", id.Expr(), schema.IntegerType{}, ""), rasql.NullItem("name", name.NullExpr(), schema.TextType{}, ""), rasql.NullItem("payload", payload.NullExpr(), schema.BytesType{}, "")}, sqliteRuntimeDecoder{schema: resultSchema})
		require.NoError(t, err)
		query := rasql.Select(relation.Source(), projection)
		values, err := rasql.All(t.Context(), executor, query)
		require.NoError(t, err)
		require.Len(t, values, 100)
		require.Equal(t, int64(1), values[0].ID)
		require.Equal(t, byte(1), values[0].Payload.Value[0])
		require.False(t, values[49].Name.Valid)
		require.False(t, values[74].Payload.Valid)
		_, err = rasql.One(t.Context(), executor, query)
		require.ErrorIs(t, err, rasql.ErrMultipleRows)
		_, found, err := rasql.Maybe(t.Context(), executor, query)
		require.ErrorIs(t, err, rasql.ErrMultipleRows)
		require.False(t, found)
		empty, err := query.Limit(0)
		require.NoError(t, err)
		_, err = rasql.One(t.Context(), executor, empty)
		require.ErrorIs(t, err, rasql.ErrNoRows)
		_, found, err = rasql.Maybe(t.Context(), executor, empty)
		require.NoError(t, err)
		require.False(t, found)
	})
}

type countingRuntimeCodec struct{ encode, decode *atomic.Int64 }

func (c countingRuntimeCodec) Encode(v any) (driver.Value, error) { c.encode.Add(1); return v, nil }
func (c countingRuntimeCodec) Decode(v any, d any) error {
	c.decode.Add(1)
	*d.(*int64) = v.(int64)
	return nil
}

type runtimeRowsExecutor struct{ rows rasql.ResultRows }

func (e runtimeRowsExecutor) Dialect() dialect.Dialect { return dialect.SQLite() }
func (e runtimeRowsExecutor) Query(context.Context, stmt.Statement) (rasql.ResultRows, error) {
	return e.rows, nil
}
func (runtimeRowsExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	return driver.RowsAffected(0), nil
}

type sqliteRuntimeRow struct {
	ID      int64
	Name    rasql.Nullable[string]
	Payload rasql.Nullable[[]byte]
}
type sqliteRuntimeDecoder struct{ schema rasql.ResultSchema }

func (d sqliteRuntimeDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (sqliteRuntimeDecoder) Presence() []rasql.Presence         { return nil }
func (sqliteRuntimeDecoder) DecodeRow(source rasql.ScanSource, result *sqliteRuntimeRow) error {
	return source.Scan(&result.ID, &result.Name, &result.Payload)
}

func TestRuntimeAPICompiles(t *testing.T) {
	directory, err := filepath.Abs(filepath.Join("testdata", "compile", "runtime_api", "positive"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "test", "./...")
	command.Dir = directory
	// GOCACHE is deliberately not overridden here: see the matching comment
	// in query_composition_internal_test.go's TestQueryAPICompiles.
	command.Env = append(command.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compile fixture failed: %v\n%s", err, output)
	}
}
