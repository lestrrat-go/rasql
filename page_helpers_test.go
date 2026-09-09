package rasql

import (
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestPrepareAndConsumePageReportsLookahead(t *testing.T) {
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
}

func TestConsumePreparedPageReturnsMapperErrorAndFinishesRows(t *testing.T) {
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
}
