package rasql

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

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

func TestR5PageAfterMixedNullOrderTraversesExactlyOnce(t *testing.T) {
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
}

func TestR5PageAfterLimitPolicyAndEmptyResult(t *testing.T) {
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
}

func TestR5PageAfterConcurrentReuseAndExactOneQuery(t *testing.T) {
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
}
