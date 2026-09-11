package rasql_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/bindplan"
	"github.com/lestrrat-go/rasql/internal/cursorcodec"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type pageAcceptanceRow struct{ ID int64 }
type pageAcceptanceDecoder struct{ schema rasql.ResultSchema }

func (d pageAcceptanceDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (d pageAcceptanceDecoder) Presence() []rasql.Presence       { return nil }
func (d pageAcceptanceDecoder) DecodeRow(source rasql.ScanSource, row *pageAcceptanceRow) error {
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
		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		table, err := rasql.ReadTableOf[pageAcceptanceRow](schema.TableDef{Name: "page_rows", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
		require.NoError(t, err)
		relation, err := rasql.SourceOf(table, "p")
		require.NoError(t, err)
		id, err := rasql.BindColumn[pageAcceptanceRow, int64](relation, "id", "")
		require.NoError(t, err)
		resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
		require.NoError(t, err)
		projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", id.Expr(), schema.IntegerType{}, "")}, pageAcceptanceDecoder{schema: resultSchema})
		require.NoError(t, err)
		query := rasql.Select(relation.Source(), projection)
		key := rasql.AscKey[pageAcceptanceRow](id.Expr(), func(row pageAcceptanceRow) int64 { return row.ID })
		spec, err := rasql.NewPageSpec([]rasql.PageKey[pageAcceptanceRow]{key}, key)
		require.NoError(t, err)
		var all []int64
		request := rasql.PageRequest{Limit: 7}
		for {
			page, pageErr := rasql.PageAfter(t.Context(), executor, query, spec, rasql.PagePolicy{DefaultLimit: 7, MaxLimit: 100}, request)
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

	// A query that was never built carries no source to order by, so paging it
	// is refused before anything reaches the database.
	t.Run("rejects paging a query that was never built", func(t *testing.T) {
		key := rasql.AscKey[int](rasql.Value(1), func(int) int { return 1 })
		spec, err := rasql.NewPageSpec([]rasql.PageKey[int]{key}, key)
		require.NoError(t, err)
		executor, _ := runtimeExecutor(t, nil)
		var q rasql.Query[int]
		_, err = rasql.PageAfter(t.Context(), executor, q, spec, rasql.DefaultPagePolicy, rasql.PageRequest{Limit: 1})
		require.Error(t, err)
	})

	t.Run("a mixed null order traverses each row exactly once", func(t *testing.T) {
		executor, query, spec := r5PageQuery(t, "(1, 2), (2, NULL), (3, 1), (4, NULL), (5, 2)")
		request := rasql.PageRequest{Limit: 2}
		var got []int64
		for {
			page, err := rasql.PageAfter(t.Context(), executor, query, spec, rasql.PagePolicy{DefaultLimit: 2, MaxLimit: 10}, request)
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
			request rasql.PageRequest
			policy  rasql.PagePolicy
		}{
			{name: "negative", request: rasql.PageRequest{Limit: -1}, policy: rasql.PagePolicy{DefaultLimit: 1, MaxLimit: 2}},
			{name: "above max", request: rasql.PageRequest{Limit: 3}, policy: rasql.PagePolicy{DefaultLimit: 1, MaxLimit: 2}},
			{name: "bad policy", request: rasql.PageRequest{}, policy: rasql.PagePolicy{DefaultLimit: 2, MaxLimit: 1}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := rasql.PageAfter(t.Context(), executor, query, spec, tc.policy, tc.request)
				require.Error(t, err)
			})
		}
		emptyQueryExecutor, emptyQuery, emptySpec := r5PageQuery(t, "(1, 1), (2, 2)")
		page, err := rasql.PageAfter(t.Context(), emptyQueryExecutor, emptyQuery, emptySpec, rasql.PagePolicy{DefaultLimit: 2, MaxLimit: 2}, rasql.PageRequest{Limit: 2, After: rasql.Cursor("not-a-cursor")})
		require.ErrorIs(t, err, rasql.ErrInvalidCursor)
		require.Empty(t, page.Values)
	})

	t.Run("concurrent reuse issues exactly one query", func(t *testing.T) {
		executor, query, spec := r5PageQuery(t, "(1, 1), (2, 2), (3, 3)")
		counted := &r5CountingExecutor{Executor: executor}
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		// WithEngineProfile re-attaches the compiler the decorator does not
		// carry, so the counting wrapper can sit in the chain from outside.
		profiled, err := rasql.WithEngineProfile(counted, profile)
		require.NoError(t, err)
		page, err := rasql.PageAfter(t.Context(), profiled, query, spec, rasql.PagePolicy{DefaultLimit: 2, MaxLimit: 4}, rasql.PageRequest{Limit: 2})
		require.NoError(t, err)
		require.Equal(t, []int64{3, 2}, []int64{page.Values[0].ID, page.Values[1].ID})
		require.Equal(t, int64(1), counted.queries.Load())

		var group sync.WaitGroup
		for i := 0; i < 8; i++ {
			group.Add(1)
			go func() {
				defer group.Done()
				value, pageErr := rasql.PageAfter(t.Context(), profiled, query, spec, rasql.PagePolicy{DefaultLimit: 1, MaxLimit: 4}, rasql.PageRequest{Limit: 1})
				require.NoError(t, pageErr)
				require.Len(t, value.Values, 1)
			}()
		}
		group.Wait()
	})

	t.Run("one query finishes early after the limit plus one", func(t *testing.T) {
		schemaValue, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.IntegerType{}})
		require.NoError(t, err)
		query, spec := r5LifecycleQuery(t, r5LifecycleDecoder{schema: schemaValue})
		executor, raw := runtimeExecutor(t, [][]any{{int64(1)}, {int64(2)}, {int64(3)}})
		page, err := rasql.PageAfter(t.Context(), executor, query, spec, rasql.PagePolicy{DefaultLimit: 1, MaxLimit: 4}, rasql.PageRequest{Limit: 1})
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
		schemaValue, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.IntegerType{}})
		require.NoError(t, err)
		query, spec := r5LifecycleQuery(t, r5LifecycleDecoder{schema: schemaValue, err: decodeErr})
		executor, raw := runtimeExecutor(t, [][]any{{int64(1)}})
		_, err = rasql.PageAfter(t.Context(), executor, query, spec, rasql.PagePolicy{DefaultLimit: 1, MaxLimit: 4}, rasql.PageRequest{Limit: 1})
		require.ErrorIs(t, err, decodeErr)
		require.ErrorIs(t, raw.last.lastFinish, decodeErr)
		require.True(t, raw.last.lastEarly)
		require.Equal(t, 1, raw.last.finished)
	})

	t.Run("preserves iteration and finish identity", func(t *testing.T) {
		schemaValue, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.IntegerType{}})
		require.NoError(t, err)
		query, spec := r5LifecycleQuery(t, r5LifecycleDecoder{schema: schemaValue})
		iterationErr := errors.New("iteration identity")
		finishErr := errors.New("finish identity")
		rows := &runtimeFakeRows{values: [][]any{{int64(1)}}, iterErr: iterationErr, finishErr: finishErr}
		executor := r5LifecycleExecutorWithRows(t, rows)
		_, err = rasql.PageAfter(t.Context(), executor, query, spec, rasql.PagePolicy{DefaultLimit: 2, MaxLimit: 4}, rasql.PageRequest{Limit: 2})
		require.ErrorIs(t, err, iterationErr)
		require.ErrorIs(t, err, finishErr)
		require.ErrorIs(t, rows.lastFinish, iterationErr)
		require.False(t, rows.lastEarly)
	})

	t.Run("preserves context cancellation identity", func(t *testing.T) {
		schemaValue, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.IntegerType{}})
		require.NoError(t, err)
		query, spec := r5LifecycleQuery(t, r5LifecycleDecoder{schema: schemaValue})
		rows := &runtimeFakeRows{values: [][]any{{int64(1)}}}
		executor := r5LifecycleExecutorWithRows(t, rows)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err = rasql.PageAfter(ctx, executor, query, spec, rasql.PagePolicy{DefaultLimit: 1, MaxLimit: 4}, rasql.PageRequest{Limit: 1})
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
		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		txDB, err := db.Begin(t.Context(), nil)
		require.NoError(t, err)
		defer func() { _ = txDB.Rollback() }()
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(txDB, profile)
		require.NoError(t, err)
		table, err := rasql.ReadTableOf[pageAcceptanceRow](schema.TableDef{Name: "transaction_page_rows", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
		require.NoError(t, err)
		relation, err := rasql.SourceOf(table, "p")
		require.NoError(t, err)
		id, err := rasql.BindColumn[pageAcceptanceRow, int64](relation, "id", "")
		require.NoError(t, err)
		resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
		require.NoError(t, err)
		projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", id.Expr(), schema.IntegerType{}, "")}, pageAcceptanceDecoder{schema: resultSchema})
		require.NoError(t, err)
		query := rasql.Select(relation.Source(), projection)
		key := rasql.AscKey[pageAcceptanceRow](id.Expr(), func(row pageAcceptanceRow) int64 { return row.ID })
		spec, err := rasql.NewPageSpec([]rasql.PageKey[pageAcceptanceRow]{key}, key)
		require.NoError(t, err)
		request := rasql.PageRequest{Limit: 2}
		var values []int64
		for {
			page, pageErr := rasql.PageAfter(t.Context(), executor, query, spec, rasql.PagePolicy{DefaultLimit: 2, MaxLimit: 3}, request)
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
	rasql.Executor
	queries atomic.Int64
}

func (e *r5CountingExecutor) Query(ctx context.Context, statement stmt.Statement) (rasql.ResultRows, error) {
	e.queries.Add(1)
	return e.Executor.Query(ctx, statement)
}

type r5PageRow struct {
	ID   int64
	Rank sql.NullInt64
}

type r5PageDecoder struct{ schema rasql.ResultSchema }

func (d r5PageDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (r5PageDecoder) Presence() []rasql.Presence         { return nil }
func (d r5PageDecoder) DecodeRow(source rasql.ScanSource, row *r5PageRow) error {
	return source.Scan(&row.ID, &row.Rank)
}

func r5PageQuery(t *testing.T, values string) (rasql.Executor, rasql.Query[r5PageRow], rasql.PageSpec[r5PageRow]) {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.Exec(`CREATE TABLE page_rows (id INTEGER NOT NULL, rank INTEGER)`)
	require.NoError(t, err)
	_, err = database.Exec("INSERT INTO page_rows (id, rank) VALUES " + values)
	require.NoError(t, err)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	table, err := rasql.ReadTableOf[r5PageRow](schema.TableDef{Name: "page_rows", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "rank", Type: schema.IntegerType{}, Nullable: true},
	}})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "p")
	require.NoError(t, err)
	id, err := rasql.BindColumn[r5PageRow, int64](relation, "id", "")
	require.NoError(t, err)
	rank, err := rasql.BindNullColumn[r5PageRow, int64](relation, "rank", "")
	require.NoError(t, err)
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "rank", Type: schema.IntegerType{}, Nullable: true})
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", id.Expr(), schema.IntegerType{}, ""), rasql.NullItem("rank", rank.NullExpr(), schema.IntegerType{}, "")}, r5PageDecoder{schema: resultSchema})
	require.NoError(t, err)
	query := rasql.Select(relation.Source(), projection)
	first := rasql.DescNullKey[r5PageRow](rank.NullExpr(), func(row r5PageRow) rasql.Nullable[int64] {
		return rasql.Nullable[int64]{Value: row.Rank.Int64, Valid: row.Rank.Valid}
	}, rasql.NullsLast)
	second := rasql.AscKey[r5PageRow](id.Expr(), func(row r5PageRow) int64 { return row.ID })
	spec, err := rasql.NewPageSpec([]rasql.PageKey[r5PageRow]{first, second}, second)
	require.NoError(t, err)
	return executor, query, spec
}

type r5LifecycleExecutor struct {
	rows *runtimeFakeRows
}

func (e *r5LifecycleExecutor) Dialect() dialect.Dialect { return dialect.SQLite() }

func (e *r5LifecycleExecutor) Query(ctx context.Context, _ stmt.Statement) (rasql.ResultRows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return e.rows, nil
}
func (*r5LifecycleExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	return driver.RowsAffected(0), nil
}
func r5LifecycleExecutorWithRows(t *testing.T, rows *runtimeFakeRows) rasql.Executor {
	t.Helper()
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.WithEngineProfile(&r5LifecycleExecutor{rows: rows}, profile)
	require.NoError(t, err)
	return executor
}

type r5LifecycleRow struct{ ID int64 }
type r5LifecycleDecoder struct {
	schema rasql.ResultSchema
	err    error
}

func (d r5LifecycleDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (r5LifecycleDecoder) Presence() []rasql.Presence         { return nil }
func (d r5LifecycleDecoder) DecodeRow(source rasql.ScanSource, row *r5LifecycleRow) error {
	if d.err != nil {
		return d.err
	}
	return source.Scan(&row.ID)
}

func r5LifecycleQuery(t *testing.T, decoder r5LifecycleDecoder) (rasql.Query[r5LifecycleRow], rasql.PageSpec[r5LifecycleRow]) {
	t.Helper()
	table, err := rasql.ReadTableOf[r5LifecycleRow](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "i")
	require.NoError(t, err)
	id, err := rasql.BindColumn[r5LifecycleRow, int64](relation, "id", "")
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("value", id.Expr(), schema.IntegerType{}, "")}, decoder)
	require.NoError(t, err)
	key := rasql.AscKey[r5LifecycleRow](id.Expr(), func(row r5LifecycleRow) int64 { return row.ID })
	spec, err := rasql.NewPageSpec([]rasql.PageKey[r5LifecycleRow]{key}, key)
	require.NoError(t, err)
	return rasql.Select(relation.Source(), projection), spec
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
		table, err := rasql.ReadTableOf[int64](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
		require.NoError(t, err)
		relation, err := rasql.SourceOf(table, "i")
		require.NoError(t, err)
		id, err := rasql.BindColumn[int64, int64](relation, "id", "")
		require.NoError(t, err)
		resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.IntegerType{}})
		require.NoError(t, err)
		projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("value", id.Expr(), schema.IntegerType{}, "")}, runtimeDecoder{schema: resultSchema})
		require.NoError(t, err)
		baseQuery := rasql.Select(relation.Source(), projection)
		// One bound value used in two predicates, so the compiled statement
		// carries the same bind twice and the cursor has to match both.
		filter := rasql.Value(int64(1))
		baseQuery = baseQuery.Where(rasql.EqualExpr(id.Expr(), filter)).Where(rasql.EqualExpr(id.Expr(), filter))
		codec := &r5CountingCursorCodec{}
		registry, err := rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{"count.page": codec})
		require.NoError(t, err)
		orderExpr, err := rasql.ValueWithCodec(int64(7), "count.page")
		require.NoError(t, err)
		orderKey := rasql.AscKey[int64](orderExpr, func(int64) int64 { return 7 })
		idKey := rasql.AscKey[int64](id.Expr(), func(value int64) int64 { return value })
		spec, err := rasql.NewPageSpec([]rasql.PageKey[int64]{orderKey, idKey}, idKey)
		require.NoError(t, err)
		raw := &runtimeFakeExecutor{rows: [][]any{{int64(1)}, {int64(2)}}, dialect: dialect.SQLite()}
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		baseExecutor, err := rasql.WithEngineProfile(raw, profile)
		require.NoError(t, err)
		executor, err := rasql.WithCodecs(baseExecutor, registry)
		require.NoError(t, err)
		first, err := rasql.PageAfter(context.Background(), executor, baseQuery, spec, rasql.PagePolicy{DefaultLimit: 1, MaxLimit: 3}, rasql.PageRequest{Limit: 1})
		require.NoError(t, err)
		require.True(t, first.HasMore)
		require.NotEmpty(t, first.Next)
		firstCount := codec.enc.Load()
		raw.mu.Lock()
		firstStatement := raw.lastStatement
		raw.mu.Unlock()
		require.Equal(t, int64(countCodecOccurrences(firstStatement.Args(), int64(7))), firstCount)
		second, err := rasql.PageAfter(context.Background(), executor, baseQuery, spec, rasql.PagePolicy{DefaultLimit: 1, MaxLimit: 3}, rasql.PageRequest{Limit: 1, After: first.Next})
		require.NoError(t, err)
		require.NotEmpty(t, second.Values)
		raw.mu.Lock()
		secondStatement := raw.lastStatement
		raw.mu.Unlock()
		secondOccurrences := countCodecOccurrences(secondStatement.Args(), int64(7))
		require.Equal(t, firstCount+int64(secondOccurrences), codec.enc.Load())
		require.Equal(t, int64(2), raw.calls.Load())
		changedFilter := baseQuery.Where(rasql.EqualValue(id.Expr(), int64(99)))
		before := raw.calls.Load()
		_, err = rasql.PageAfter(context.Background(), executor, changedFilter, spec, rasql.PagePolicy{DefaultLimit: 1, MaxLimit: 3}, rasql.PageRequest{Limit: 1, After: first.Next})
		require.ErrorIs(t, err, rasql.ErrInvalidCursor)
		require.Equal(t, before, raw.calls.Load())
	})

	t.Run("base occurrences match an ordered subsequence by identity and codec", func(t *testing.T) {
		first := bindplan.ID(11)
		second := bindplan.ID(12)
		base := bindplan.Compiled{
			Statement: stmt.New(sqltext.Text("SELECT ? WHERE x = ? ORDER BY y = ?"), sql.Named("filter", int64(1)), sql.Named("order", int64(2)), int64(3)),
			Slots:     []bindplan.Slot{{ID: first, Codec: "filter.codec"}, {ID: second, Codec: "order.codec"}, {ID: second, Codec: "order.codec"}},
		}
		paged := bindplan.Compiled{
			Statement: stmt.New(sqltext.Text("SELECT ? WHERE x = ? AND key > ? ORDER BY y = ? AND z = ?"), int64(99), sql.Named("filter", int64(1)), int64(9), sql.Named("order", int64(2)), sql.Named("order", int64(2))),
			Slots:     []bindplan.Slot{{ID: bindplan.ID(99)}, {ID: first, Codec: "filter.codec"}, {ID: bindplan.ID(98)}, {ID: second, Codec: "order.codec"}, {ID: second, Codec: "order.codec"}},
		}
		indexes, err := bindplan.MatchBaseOccurrences(base, paged)
		require.NoError(t, err)
		require.Equal(t, []int{1, 3, 4}, indexes)
		require.Equal(t, "filter", paged.Statement.Args()[indexes[0]].(sql.NamedArg).Name)
		require.Equal(t, "order", paged.Statement.Args()[indexes[1]].(sql.NamedArg).Name)
	})

	t.Run("base occurrences distinguish codec and reordered binds", func(t *testing.T) {
		base := bindplan.Compiled{Statement: stmt.New(sqltext.Text("SELECT ?"), 1), Slots: []bindplan.Slot{{ID: bindplan.ID(21), Codec: "a"}}}
		wrongCodec := bindplan.Compiled{Statement: stmt.New(sqltext.Text("SELECT ?"), 1), Slots: []bindplan.Slot{{ID: bindplan.ID(21), Codec: "b"}}}
		_, err := bindplan.MatchBaseOccurrences(base, wrongCodec)
		require.Error(t, err)
		require.Contains(t, err.Error(), "codec differs")
		missing := bindplan.Compiled{Statement: stmt.New(sqltext.Text("SELECT ?"), 1), Slots: []bindplan.Slot{{ID: bindplan.ID(22), Codec: "a"}}}
		_, err = bindplan.MatchBaseOccurrences(base, missing)
		require.Error(t, err)
		require.Contains(t, err.Error(), "missing or reordered")
		zero := bindplan.Compiled{Statement: stmt.New(sqltext.Text("SELECT ?"), 1), Slots: []bindplan.Slot{{ID: 0}}}
		_, err = bindplan.MatchBaseOccurrences(zero, wrongCodec)
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

func TestPreparedPage(t *testing.T) {
	t.Run("reports the lookahead row", func(t *testing.T) {
		schemaValue, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.IntegerType{}})
		require.NoError(t, err)
		query, spec := r5LifecycleQuery(t, r5LifecycleDecoder{schema: schemaValue})
		executor, raw := runtimeExecutor(t, [][]any{{int64(1)}, {int64(2)}, {int64(3)}})

		prepared, err := rasql.Q1PreparePageAfter(executor, query, spec, rasql.PagePolicy{DefaultLimit: 1, MaxLimit: 4}, rasql.PageRequest{Limit: 1})
		require.NoError(t, err)
		var rows []int64
		var kept []bool
		page, err := rasql.Q1ConsumePreparedPage(t.Context(), executor, prepared, func(row r5LifecycleRow, retain bool) error {
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
		schemaValue, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.IntegerType{}})
		require.NoError(t, err)
		query, spec := r5LifecycleQuery(t, r5LifecycleDecoder{schema: schemaValue})
		executor, raw := runtimeExecutor(t, [][]any{{int64(1)}, {int64(2)}})
		prepared, err := rasql.Q1PreparePageAfter(executor, query, spec, rasql.PagePolicy{DefaultLimit: 2, MaxLimit: 4}, rasql.PageRequest{Limit: 2})
		require.NoError(t, err)
		mapperErr := errors.New("mapper identity")
		_, err = rasql.Q1ConsumePreparedPage(t.Context(), executor, prepared, func(r5LifecycleRow, bool) error {
			return mapperErr
		})
		require.ErrorIs(t, err, mapperErr)
		require.ErrorIs(t, raw.last.lastFinish, mapperErr)
		require.True(t, raw.last.lastEarly)
	})
}
