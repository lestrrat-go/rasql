package rasql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

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

func TestR5PageAfterUsesOneQueryAndFinishesEarlyAfterLimitPlusOne(t *testing.T) {
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
}

func TestR5PageAfterPreservesDecodeAndFinishErrors(t *testing.T) {
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
}

func TestR5PageAfterPreservesIterationAndFinishIdentity(t *testing.T) {
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
}

func TestR5PageAfterUsesContextCancellationIdentity(t *testing.T) {
	schemaValue, err := NewResultSchema(ResultColumn{Name: "value", Type: schema.IntegerType{}})
	require.NoError(t, err)
	query, spec := r5LifecycleQuery(t, r5LifecycleDecoder{schema: schemaValue})
	rows := &runtimeFakeRows{values: [][]any{{int64(1)}}}
	executor := r5LifecycleExecutorWithRows(t, rows)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = PageAfter(ctx, executor, query, spec, PagePolicy{DefaultLimit: 1, MaxLimit: 4}, PageRequest{Limit: 1})
	require.ErrorIs(t, err, context.Canceled)
}
