package rasql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/bindplan"
	"github.com/lestrrat-go/rasql/internal/cursorcodec"
	"github.com/lestrrat-go/rasql/internal/querycompile"
	querypkg "github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type pageAcceptanceRow struct{ ID int64 }
type pageAcceptanceDecoder struct{ schema ResultSchema }

func (d pageAcceptanceDecoder) ResultSchema() ResultSchema { return d.schema }
func (d pageAcceptanceDecoder) Presence() []Presence       { return nil }
func (d pageAcceptanceDecoder) DecodeRow(source ScanSource, row *pageAcceptanceRow) error {
	return source.Scan(&row.ID)
}

func TestPageAfter(t *testing.T) {
	t.Run("traverses every SQLite row", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		_, err = database.Exec(`CREATE TABLE page_rows (id INTEGER NOT NULL)`)
		require.NoError(t, err)
		for i := 1; i <= 101; i++ {
			_, err = database.Exec(`INSERT INTO page_rows (id) VALUES (?)`, i)
			require.NoError(t, err)
		}
		db, err := New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := AsExecutor(db, profile)
		require.NoError(t, err)
		table, err := ReadTableOf[pageAcceptanceRow](schema.TableDef{Name: "page_rows", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
		require.NoError(t, err)
		relation, err := SourceOf(table, "p")
		require.NoError(t, err)
		id, err := BindColumn[pageAcceptanceRow, int64](relation, "id", "")
		require.NoError(t, err)
		resultSchema, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}})
		require.NoError(t, err)
		projection, err := NewProjection([]ProjectionItem{Item("id", id.Expr(), schema.IntegerType{}, "")}, pageAcceptanceDecoder{schema: resultSchema})
		require.NoError(t, err)
		query := Select(relation.Source(), projection)
		key := AscKey[pageAcceptanceRow](id.Expr(), func(row pageAcceptanceRow) int64 { return row.ID })
		spec, err := NewPageSpec([]PageKey[pageAcceptanceRow]{key}, key)
		require.NoError(t, err)
		var all []int64
		request := PageRequest{Limit: 7}
		for {
			page, pageErr := PageAfter(t.Context(), executor, query, spec, PagePolicy{DefaultLimit: 7, MaxLimit: 100}, request)
			require.NoError(t, pageErr)
			for _, row := range page.Values {
				all = append(all, row.ID)
			}
			if !page.HasMore {
				break
			}
			request.After = page.Next
		}
		require.Len(t, all, 101)
		for i, value := range all {
			require.Equal(t, int64(i+1), value)
		}
	})

	t.Run("rejects paging a base query", func(t *testing.T) {
		var q Query[int]
		_, err := q.withKeysetOrder([]OrderTerm{{}})
		require.Error(t, err)
	})

	t.Run("a mixed null order traverses each row exactly once", func(t *testing.T) {
		executor, query, spec := r5PageQuery(t, "(1, 2), (2, NULL), (3, 1), (4, NULL), (5, 2)")
		request := PageRequest{Limit: 2}
		var got []int64
		for {
			page, err := PageAfter(t.Context(), executor, query, spec, PagePolicy{DefaultLimit: 2, MaxLimit: 10}, request)
			require.NoError(t, err)
			for _, row := range page.Values {
				got = append(got, row.ID)
			}
			if !page.HasMore {
				break
			}
			require.NotEmpty(t, page.Next)
			request.After = page.Next
		}
		require.Equal(t, []int64{1, 5, 3, 2, 4}, got)
	})

	t.Run("limit policy and empty result", func(t *testing.T) {
		executor, query, spec := r5PageQuery(t, "(1, 1), (2, 2)")
		for _, tc := range []struct {
			name    string
			request PageRequest
			policy  PagePolicy
		}{
			{name: "negative", request: PageRequest{Limit: -1}, policy: PagePolicy{DefaultLimit: 1, MaxLimit: 2}},
			{name: "above max", request: PageRequest{Limit: 3}, policy: PagePolicy{DefaultLimit: 1, MaxLimit: 2}},
			{name: "bad policy", request: PageRequest{}, policy: PagePolicy{DefaultLimit: 2, MaxLimit: 1}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := PageAfter(t.Context(), executor, query, spec, tc.policy, tc.request)
				require.Error(t, err)
			})
		}
		emptyQueryExecutor, emptyQuery, emptySpec := r5PageQuery(t, "(1, 1), (2, 2)")
		page, err := PageAfter(t.Context(), emptyQueryExecutor, emptyQuery, emptySpec, PagePolicy{DefaultLimit: 2, MaxLimit: 2}, PageRequest{Limit: 2, After: Cursor("not-a-cursor")})
		require.ErrorIs(t, err, ErrInvalidCursor)
		require.Empty(t, page.Values)
	})

	t.Run("concurrent reuse issues exactly one query", func(t *testing.T) {
		executor, query, spec := r5PageQuery(t, "(1, 1), (2, 2), (3, 3)")
		counted := &r5CountingExecutor{Executor: executor}
		page, err := PageAfter(t.Context(), counted, query, spec, PagePolicy{DefaultLimit: 2, MaxLimit: 4}, PageRequest{Limit: 2})
		require.NoError(t, err)
		require.Equal(t, []int64{3, 2}, []int64{page.Values[0].ID, page.Values[1].ID})
		require.Equal(t, int64(1), counted.queries.Load())

		var group sync.WaitGroup
		for i := 0; i < 8; i++ {
			group.Add(1)
			go func() {
				defer group.Done()
				value, pageErr := PageAfter(t.Context(), counted, query, spec, PagePolicy{DefaultLimit: 1, MaxLimit: 4}, PageRequest{Limit: 1})
				require.NoError(t, pageErr)
				require.Len(t, value.Values, 1)
			}()
		}
		group.Wait()
	})

	t.Run("one query finishes early after the limit plus one", func(t *testing.T) {
		schemaValue, err := NewResultSchema(ResultColumn{Name: "value", Type: schema.IntegerType{}})
		require.NoError(t, err)
		query, spec := r5LifecycleQuery(t, r5LifecycleDecoder{schema: schemaValue})
		executor := runtimeExecutor(t, [][]any{{int64(1)}, {int64(2)}, {int64(3)}})
		raw := executor.(profiledExecutor).Executor.(*runtimeFakeExecutor)
		page, err := PageAfter(t.Context(), executor, query, spec, PagePolicy{DefaultLimit: 1, MaxLimit: 4}, PageRequest{Limit: 1})
		require.NoError(t, err)
		require.Equal(t, []int64{1}, []int64{page.Values[0].ID})
		require.True(t, page.HasMore)
		require.Equal(t, int64(1), raw.calls.Load())
		require.Equal(t, 2, raw.last.recorded)
		require.Equal(t, 1, raw.last.finished)
		require.True(t, raw.last.lastEarly)
		require.NoError(t, raw.last.lastFinish)
	})

	t.Run("preserves decode and finish errors", func(t *testing.T) {
		decodeErr := errors.New("decode identity")
		schemaValue, err := NewResultSchema(ResultColumn{Name: "value", Type: schema.IntegerType{}})
		require.NoError(t, err)
		query, spec := r5LifecycleQuery(t, r5LifecycleDecoder{schema: schemaValue, err: decodeErr})
		executor := runtimeExecutor(t, [][]any{{int64(1)}})
		raw := executor.(profiledExecutor).Executor.(*runtimeFakeExecutor)
		_, err = PageAfter(t.Context(), executor, query, spec, PagePolicy{DefaultLimit: 1, MaxLimit: 4}, PageRequest{Limit: 1})
		require.ErrorIs(t, err, decodeErr)
		require.ErrorIs(t, raw.last.lastFinish, decodeErr)
		require.True(t, raw.last.lastEarly)
		require.Equal(t, 1, raw.last.finished)
	})

	t.Run("preserves iteration and finish identity", func(t *testing.T) {
		schemaValue, err := NewResultSchema(ResultColumn{Name: "value", Type: schema.IntegerType{}})
		require.NoError(t, err)
		query, spec := r5LifecycleQuery(t, r5LifecycleDecoder{schema: schemaValue})
		iterationErr := errors.New("iteration identity")
		finishErr := errors.New("finish identity")
		rows := &runtimeFakeRows{values: [][]any{{int64(1)}}, iterErr: iterationErr, finishErr: finishErr}
		executor := r5LifecycleExecutorWithRows(t, rows)
		_, err = PageAfter(t.Context(), executor, query, spec, PagePolicy{DefaultLimit: 2, MaxLimit: 4}, PageRequest{Limit: 2})
		require.ErrorIs(t, err, iterationErr)
		require.ErrorIs(t, err, finishErr)
		require.ErrorIs(t, rows.lastFinish, iterationErr)
		require.False(t, rows.lastEarly)
	})

	t.Run("preserves context cancellation identity", func(t *testing.T) {
		schemaValue, err := NewResultSchema(ResultColumn{Name: "value", Type: schema.IntegerType{}})
		require.NoError(t, err)
		query, spec := r5LifecycleQuery(t, r5LifecycleDecoder{schema: schemaValue})
		rows := &runtimeFakeRows{values: [][]any{{int64(1)}}}
		executor := r5LifecycleExecutorWithRows(t, rows)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err = PageAfter(ctx, executor, query, spec, PagePolicy{DefaultLimit: 1, MaxLimit: 4}, PageRequest{Limit: 1})
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("an explicit transaction uses one isolation scope", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		database.SetMaxOpenConns(1)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		_, err = database.ExecContext(t.Context(), `CREATE TABLE transaction_page_rows (id INTEGER NOT NULL)`)
		require.NoError(t, err)
		_, err = database.ExecContext(t.Context(), `INSERT INTO transaction_page_rows (id) VALUES (1),(2),(3),(4),(5)`)
		require.NoError(t, err)
		db, err := New(database, dialect.SQLite())
		require.NoError(t, err)
		txDB, err := db.Begin(t.Context(), nil)
		require.NoError(t, err)
		defer func() { _ = txDB.Rollback() }()
		profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := AsExecutor(txDB, profile)
		require.NoError(t, err)
		table, err := ReadTableOf[pageAcceptanceRow](schema.TableDef{Name: "transaction_page_rows", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
		require.NoError(t, err)
		relation, err := SourceOf(table, "p")
		require.NoError(t, err)
		id, err := BindColumn[pageAcceptanceRow, int64](relation, "id", "")
		require.NoError(t, err)
		resultSchema, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}})
		require.NoError(t, err)
		projection, err := NewProjection([]ProjectionItem{Item("id", id.Expr(), schema.IntegerType{}, "")}, pageAcceptanceDecoder{schema: resultSchema})
		require.NoError(t, err)
		query := Select(relation.Source(), projection)
		key := AscKey[pageAcceptanceRow](id.Expr(), func(row pageAcceptanceRow) int64 { return row.ID })
		spec, err := NewPageSpec([]PageKey[pageAcceptanceRow]{key}, key)
		require.NoError(t, err)
		request := PageRequest{Limit: 2}
		var values []int64
		for {
			page, pageErr := PageAfter(t.Context(), executor, query, spec, PagePolicy{DefaultLimit: 2, MaxLimit: 3}, request)
			require.NoError(t, pageErr)
			for _, row := range page.Values {
				values = append(values, row.ID)
			}
			if !page.HasMore {
				break
			}
			request.After = page.Next
		}
		require.Equal(t, []int64{1, 2, 3, 4, 5}, values)
		require.NoError(t, txDB.Commit())
	})
}

type r5CountingExecutor struct {
	Executor
	queries atomic.Int64
}

func (e *r5CountingExecutor) Query(ctx context.Context, statement stmt.Statement) (ResultRows, error) {
	e.queries.Add(1)
	return e.Executor.Query(ctx, statement)
}

func (e *r5CountingExecutor) queryCompiler() *querycompile.Compiler {
	return e.Executor.(compilerProvider).queryCompiler()
}

type r5PageRow struct {
	ID   int64
	Rank sql.NullInt64
}

type r5PageDecoder struct{ schema ResultSchema }

func (d r5PageDecoder) ResultSchema() ResultSchema { return d.schema }
func (r5PageDecoder) Presence() []Presence         { return nil }
func (d r5PageDecoder) DecodeRow(source ScanSource, row *r5PageRow) error {
	return source.Scan(&row.ID, &row.Rank)
}

func r5PageQuery(t *testing.T, values string) (Executor, Query[r5PageRow], PageSpec[r5PageRow]) {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.Exec(`CREATE TABLE page_rows (id INTEGER NOT NULL, rank INTEGER)`)
	require.NoError(t, err)
	_, err = database.Exec("INSERT INTO page_rows (id, rank) VALUES " + values)
	require.NoError(t, err)
	db, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	table, err := ReadTableOf[r5PageRow](schema.TableDef{Name: "page_rows", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "rank", Type: schema.IntegerType{}, Nullable: true},
	}})
	require.NoError(t, err)
	relation, err := SourceOf(table, "p")
	require.NoError(t, err)
	id, err := BindColumn[r5PageRow, int64](relation, "id", "")
	require.NoError(t, err)
	rank, err := BindNullColumn[r5PageRow, int64](relation, "rank", "")
	require.NoError(t, err)
	resultSchema, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}}, ResultColumn{Name: "rank", Type: schema.IntegerType{}, Nullable: true})
	require.NoError(t, err)
	projection, err := NewProjection([]ProjectionItem{Item("id", id.Expr(), schema.IntegerType{}, ""), NullItem("rank", rank.NullExpr(), schema.IntegerType{}, "")}, r5PageDecoder{schema: resultSchema})
	require.NoError(t, err)
	query := Select(relation.Source(), projection)
	first := DescNullKey[r5PageRow](rank.NullExpr(), func(row r5PageRow) Nullable[int64] {
		return Nullable[int64]{Value: row.Rank.Int64, Valid: row.Rank.Valid}
	}, NullsLast)
	second := AscKey[r5PageRow](id.Expr(), func(row r5PageRow) int64 { return row.ID })
	spec, err := NewPageSpec([]PageKey[r5PageRow]{first, second}, second)
	require.NoError(t, err)
	return executor, query, spec
}

type r5LifecycleExecutor struct {
	rows *runtimeFakeRows
}

func (e *r5LifecycleExecutor) Dialect() dialect.Dialect { return dialect.SQLite() }

func (e *r5LifecycleExecutor) Query(ctx context.Context, _ stmt.Statement) (ResultRows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return e.rows, nil
}
func (*r5LifecycleExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	return driver.RowsAffected(0), nil
}
func r5LifecycleExecutorWithRows(t *testing.T, rows *runtimeFakeRows) Executor {
	t.Helper()
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	compiler, err := profile.queryCompiler(dialect.SQLite())
	require.NoError(t, err)
	return profiledExecutor{Executor: &r5LifecycleExecutor{rows: rows}, compiler: compiler}
}

type r5LifecycleRow struct{ ID int64 }
type r5LifecycleDecoder struct {
	schema ResultSchema
	err    error
}

func (d r5LifecycleDecoder) ResultSchema() ResultSchema { return d.schema }
func (r5LifecycleDecoder) Presence() []Presence         { return nil }
func (d r5LifecycleDecoder) DecodeRow(source ScanSource, row *r5LifecycleRow) error {
	if d.err != nil {
		return d.err
	}
	return source.Scan(&row.ID)
}

func r5LifecycleQuery(t *testing.T, decoder r5LifecycleDecoder) (Query[r5LifecycleRow], PageSpec[r5LifecycleRow]) {
	t.Helper()
	table, err := ReadTableOf[r5LifecycleRow](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	relation, err := SourceOf(table, "i")
	require.NoError(t, err)
	id, err := BindColumn[r5LifecycleRow, int64](relation, "id", "")
	require.NoError(t, err)
	projection, err := NewProjection([]ProjectionItem{Item("value", id.Expr(), schema.IntegerType{}, "")}, decoder)
	require.NoError(t, err)
	key := AscKey[r5LifecycleRow](id.Expr(), func(row r5LifecycleRow) int64 { return row.ID })
	spec, err := NewPageSpec([]PageKey[r5LifecycleRow]{key}, key)
	require.NoError(t, err)
	return Select(relation.Source(), projection), spec
}

type r5CountingCursorCodec struct{ enc atomic.Int64 }

func (c *r5CountingCursorCodec) Encode(value any) (driver.Value, error) {
	c.enc.Add(1)
	return value, nil
}
func (*r5CountingCursorCodec) Decode(value any, destination any) error {
	*destination.(*int64) = value.(int64)
	return nil
}
func (*r5CountingCursorCodec) EncodeCursor(value any) ([]byte, error) {
	return cursorcodec.EncodeValue(value)
}
func (*r5CountingCursorCodec) DecodeCursor(value []byte) (any, error) {
	return cursorcodec.DecodeValue(value, reflect.TypeOf(int64(0)))
}

func TestPageBinds(t *testing.T) {
	t.Run("named codec occurrences are counted without re-encoding the fingerprint or rows", func(t *testing.T) {
		table, err := ReadTableOf[int64](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
		require.NoError(t, err)
		relation, err := SourceOf(table, "i")
		require.NoError(t, err)
		id, err := BindColumn[int64, int64](relation, "id", "")
		require.NoError(t, err)
		resultSchema, err := NewResultSchema(ResultColumn{Name: "value", Type: schema.IntegerType{}})
		require.NoError(t, err)
		projection, err := NewProjection([]ProjectionItem{Item("value", id.Expr(), schema.IntegerType{}, "")}, runtimeDecoder{schema: resultSchema})
		require.NoError(t, err)
		baseQuery := Select(relation.Source(), projection)
		filterID := bindplan.NextID()
		filter := Expr[int64]{node: querypkg.Bind(bindToken{ID: filterID, Value: sql.Named("filter", int64(1)), Copy: func() (any, error) { return sql.Named("filter", int64(1)), nil }})}
		baseQuery = baseQuery.Where(Predicate{node: querypkg.Equal(id.Expr().node, filter.node)}).Where(Predicate{node: querypkg.Equal(id.Expr().node, filter.node)})
		codec := &r5CountingCursorCodec{}
		registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"count.page": codec})
		require.NoError(t, err)
		orderExpr, err := ValueWithCodec(int64(7), "count.page")
		require.NoError(t, err)
		orderKey := AscKey[int64](orderExpr, func(int64) int64 { return 7 })
		idKey := AscKey[int64](id.Expr(), func(value int64) int64 { return value })
		spec, err := NewPageSpec([]PageKey[int64]{orderKey, idKey}, idKey)
		require.NoError(t, err)
		raw := &runtimeFakeExecutor{rows: [][]any{{int64(1)}, {int64(2)}}, dialect: dialect.SQLite()}
		profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		baseExecutor, err := WithEngineProfile(raw, profile)
		require.NoError(t, err)
		executor, err := WithCodecs(baseExecutor, registry)
		require.NoError(t, err)
		first, err := PageAfter(context.Background(), executor, baseQuery, spec, PagePolicy{DefaultLimit: 1, MaxLimit: 3}, PageRequest{Limit: 1})
		require.NoError(t, err)
		require.True(t, first.HasMore)
		require.NotEmpty(t, first.Next)
		firstCount := codec.enc.Load()
		raw.mu.Lock()
		firstStatement := raw.lastStatement
		raw.mu.Unlock()
		require.Equal(t, int64(countCodecOccurrences(firstStatement.Args(), int64(7))), firstCount)
		second, err := PageAfter(context.Background(), executor, baseQuery, spec, PagePolicy{DefaultLimit: 1, MaxLimit: 3}, PageRequest{Limit: 1, After: first.Next})
		require.NoError(t, err)
		require.NotEmpty(t, second.Values)
		raw.mu.Lock()
		secondStatement := raw.lastStatement
		raw.mu.Unlock()
		secondOccurrences := countCodecOccurrences(secondStatement.Args(), int64(7))
		require.Equal(t, firstCount+int64(secondOccurrences), codec.enc.Load())
		require.Equal(t, int64(2), raw.calls.Load())
		changedFilterValue := Value(int64(99))
		changedFilter := baseQuery.Where(Predicate{node: querypkg.Equal(id.Expr().node, changedFilterValue.node)})
		before := raw.calls.Load()
		_, err = PageAfter(context.Background(), executor, changedFilter, spec, PagePolicy{DefaultLimit: 1, MaxLimit: 3}, PageRequest{Limit: 1, After: first.Next})
		require.ErrorIs(t, err, ErrInvalidCursor)
		require.Equal(t, before, raw.calls.Load())
	})

	t.Run("base occurrences match an ordered subsequence by identity and codec", func(t *testing.T) {
		first := bindID(11)
		second := bindID(12)
		base := compiledQuery{
			Statement: stmt.New(sqltext.Text("SELECT ? WHERE x = ? ORDER BY y = ?"), sql.Named("filter", int64(1)), sql.Named("order", int64(2)), int64(3)),
			Slots:     []bindSlot{{ID: first, Codec: "filter.codec"}, {ID: second, Codec: "order.codec"}, {ID: second, Codec: "order.codec"}},
		}
		paged := compiledQuery{
			Statement: stmt.New(sqltext.Text("SELECT ? WHERE x = ? AND key > ? ORDER BY y = ? AND z = ?"), int64(99), sql.Named("filter", int64(1)), int64(9), sql.Named("order", int64(2)), sql.Named("order", int64(2))),
			Slots:     []bindSlot{{ID: bindID(99)}, {ID: first, Codec: "filter.codec"}, {ID: bindID(98)}, {ID: second, Codec: "order.codec"}, {ID: second, Codec: "order.codec"}},
		}
		indexes, err := matchBaseOccurrences(base, paged)
		require.NoError(t, err)
		require.Equal(t, []int{1, 3, 4}, indexes)
		require.Equal(t, "filter", paged.Statement.Args()[indexes[0]].(sql.NamedArg).Name)
		require.Equal(t, "order", paged.Statement.Args()[indexes[1]].(sql.NamedArg).Name)
	})

	t.Run("base occurrences distinguish codec and reordered binds", func(t *testing.T) {
		base := compiledQuery{Statement: stmt.New(sqltext.Text("SELECT ?"), 1), Slots: []bindSlot{{ID: bindID(21), Codec: "a"}}}
		wrongCodec := compiledQuery{Statement: stmt.New(sqltext.Text("SELECT ?"), 1), Slots: []bindSlot{{ID: bindID(21), Codec: "b"}}}
		_, err := matchBaseOccurrences(base, wrongCodec)
		require.Error(t, err)
		require.Contains(t, err.Error(), "codec differs")
		missing := compiledQuery{Statement: stmt.New(sqltext.Text("SELECT ?"), 1), Slots: []bindSlot{{ID: bindID(22), Codec: "a"}}}
		_, err = matchBaseOccurrences(base, missing)
		require.Error(t, err)
		require.Contains(t, err.Error(), "missing or reordered")
		zero := compiledQuery{Statement: stmt.New(sqltext.Text("SELECT ?"), 1), Slots: []bindSlot{{ID: 0}}}
		_, err = matchBaseOccurrences(zero, wrongCodec)
		require.Error(t, err)
		require.Contains(t, err.Error(), "no identity")
	})
}

func countCodecOccurrences(args []any, value int64) int {
	count := 0
	for _, arg := range args {
		if named, ok := arg.(sql.NamedArg); ok {
			if named.Value == value {
				count++
			}
			continue
		}
		if arg == value {
			count++
		}
	}
	return count
}

func TestPageFingerprint(t *testing.T) {
	t.Run("preserves float bind bits", func(t *testing.T) {
		statementSchema := mustRuntimeSchema(t, ResultColumn{Name: "id", Type: schema.IntegerType{}})
		keys := []*pageKey[int]{{direction: PageAscending, term: OrderTerm{source: "items"}}}
		positiveZero := stmt.New(sqltext.Text("SELECT id FROM items WHERE score = ?"), float64(0))
		negativeZero := stmt.New(sqltext.Text("SELECT id FROM items WHERE score = ?"), math.Copysign(0, -1))
		positiveNaN := stmt.New(sqltext.Text("SELECT id FROM items WHERE score = ?"), math.Float64frombits(0x7ff8000000000001))
		negativeNaN := stmt.New(sqltext.Text("SELECT id FROM items WHERE score = ?"), math.Float64frombits(0xfff8000000000001))

		positiveZeroFingerprint, err := pageFingerprint("sqlite", positiveZero.SQL(), statementSchema, positiveZero, keys, []int{0})
		require.NoError(t, err)
		negativeZeroFingerprint, err := pageFingerprint("sqlite", negativeZero.SQL(), statementSchema, negativeZero, keys, []int{0})
		require.NoError(t, err)
		positiveNaNFingerprint, err := pageFingerprint("sqlite", positiveNaN.SQL(), statementSchema, positiveNaN, keys, []int{0})
		require.NoError(t, err)
		negativeNaNFingerprint, err := pageFingerprint("sqlite", negativeNaN.SQL(), statementSchema, negativeNaN, keys, []int{0})
		require.NoError(t, err)

		require.NotEqual(t, positiveZeroFingerprint, negativeZeroFingerprint)
		require.NotEqual(t, positiveNaNFingerprint, negativeNaNFingerprint)
	})

	t.Run("changes with the filter but ignores cursor and limit binds", func(t *testing.T) {
		statement := stmt.New(sqltext.Text("SELECT id FROM items WHERE category = ? AND id > ? LIMIT ?"), "books", int64(7), int64(3))
		keys := []*pageKey[int]{&pageKey[int]{direction: PageAscending, term: OrderTerm{source: "items"}}}
		first, err := pageFingerprint("sqlite", "SELECT id FROM items WHERE category = ? ORDER BY id", mustRuntimeSchema(t, ResultColumn{Name: "id", Type: schema.IntegerType{}}), statement, keys, []int{0})
		require.NoError(t, err)
		changed := stmt.New(statement.Text(), "music", int64(7), int64(99))
		second, err := pageFingerprint("sqlite", "SELECT id FROM items WHERE category = ? ORDER BY id", mustRuntimeSchema(t, ResultColumn{Name: "id", Type: schema.IntegerType{}}), changed, keys, []int{0})
		require.NoError(t, err)
		require.NotEqual(t, first, second)
		require.False(t, strings.Contains(statement.SQL(), "cursor"))
	})

	t.Run("includes canonical column parameters", func(t *testing.T) {
		key := &pageKey[int]{direction: PageAscending, term: OrderTerm{source: "items"}}
		statement := stmt.New(sqltext.Text("SELECT id FROM items"))
		base := func(column schema.ColumnType) [32]byte {
			fingerprint, err := pageFingerprint("sqlite", statement.SQL(), mustRuntimeSchema(t, ResultColumn{Name: "value", Type: column}), statement, []*pageKey[int]{key}, nil)
			require.NoError(t, err)
			return fingerprint
		}
		require.NotEqual(t, base(schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2)}), base(schema.DecimalType{Precision: 12, Scale: schema.NewDecimalScale(2)}))
		require.NotEqual(t, base(schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2)}), base(schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(3)}))
		require.NotEqual(t, base(schema.TextType{Width: schema.NewTextWidth(10)}), base(schema.TextType{Width: schema.NewTextWidth(20)}))
		require.NotEqual(t, base(schema.TextType{Width: schema.NewTextWidth(10)}), base(schema.TextType{Width: schema.NewTextWidth(10), Fixed: true}))
		require.NotEqual(t, base(schema.IntegerType{}), base(schema.IntegerType{Unsigned: true}))
		require.Equal(t, base(schema.IntegerType{}), base(schema.IntegerType{}))
	})
}

func TestPreparedPage(t *testing.T) {
	t.Run("reports the lookahead row", func(t *testing.T) {
		schemaValue, err := NewResultSchema(ResultColumn{Name: "value", Type: schema.IntegerType{}})
		require.NoError(t, err)
		query, spec := r5LifecycleQuery(t, r5LifecycleDecoder{schema: schemaValue})
		executor := runtimeExecutor(t, [][]any{{int64(1)}, {int64(2)}, {int64(3)}})
		raw := executor.(profiledExecutor).Executor.(*runtimeFakeExecutor)

		prepared, err := preparePageAfter(executor, query, spec, PagePolicy{DefaultLimit: 1, MaxLimit: 4}, PageRequest{Limit: 1})
		require.NoError(t, err)
		var rows []int64
		var kept []bool
		page, err := consumePreparedPage(t.Context(), executor, prepared, func(row r5LifecycleRow, retain bool) error {
			rows = append(rows, row.ID)
			kept = append(kept, retain)
			return nil
		})
		require.NoError(t, err)
		require.Equal(t, []int64{1}, []int64{page.Values[0].ID})
		require.Equal(t, []int64{1, 2}, rows)
		require.Equal(t, []bool{true, false}, kept)
		require.True(t, page.HasMore)
		require.NotEmpty(t, page.Next)
		require.Equal(t, 2, raw.last.recorded)
		require.True(t, raw.last.lastEarly)
	})

	t.Run("returns a mapper error and finishes rows", func(t *testing.T) {
		schemaValue, err := NewResultSchema(ResultColumn{Name: "value", Type: schema.IntegerType{}})
		require.NoError(t, err)
		query, spec := r5LifecycleQuery(t, r5LifecycleDecoder{schema: schemaValue})
		executor := runtimeExecutor(t, [][]any{{int64(1)}, {int64(2)}})
		raw := executor.(profiledExecutor).Executor.(*runtimeFakeExecutor)
		prepared, err := preparePageAfter(executor, query, spec, PagePolicy{DefaultLimit: 2, MaxLimit: 4}, PageRequest{Limit: 2})
		require.NoError(t, err)
		mapperErr := errors.New("mapper identity")
		_, err = consumePreparedPage(t.Context(), executor, prepared, func(r5LifecycleRow, bool) error {
			return mapperErr
		})
		require.ErrorIs(t, err, mapperErr)
		require.ErrorIs(t, raw.last.lastFinish, mapperErr)
		require.True(t, raw.last.lastEarly)
	})
}
