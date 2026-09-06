package rasql_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type bulkTestRow struct{}

type bulkCall struct {
	sql  string
	args []any
}

type bulkRecordingHandle struct {
	calls []bulkCall
}

type bulkFailHandle struct {
	calls  []bulkCall
	failAt int
	err    error
}

func (h *bulkRecordingHandle) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, fmt.Errorf("unexpected query call")
}

func (h *bulkRecordingHandle) ExecContext(_ context.Context, statement string, args ...any) (sql.Result, error) {
	h.calls = append(h.calls, bulkCall{sql: statement, args: append([]any(nil), args...)})
	return bulkResult{}, nil
}

func (h *bulkFailHandle) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, fmt.Errorf("unexpected query call")
}

func (h *bulkFailHandle) ExecContext(_ context.Context, statement string, args ...any) (sql.Result, error) {
	h.calls = append(h.calls, bulkCall{sql: statement, args: append([]any(nil), args...)})
	if len(h.calls) == h.failAt {
		return nil, h.err
	}
	return bulkResult{}, nil
}

type bulkResult struct{}

func (bulkResult) LastInsertId() (int64, error) { return 1, nil }
func (bulkResult) RowsAffected() (int64, error) { return 1, nil }

func bulkTestTable() (rasql.Table[bulkTestRow], query.TypedColumn[bulkTestRow, string], query.TypedColumn[bulkTestRow, int64], query.NullableColumn[bulkTestRow, *string]) {
	table := rasql.MustTableOf[bulkTestRow](schema.TableDef{
		Name:       "items",
		PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}, Identity: schema.IdentityAlways},
			{Name: "a", Type: schema.TextType{}, Default: "'a'"},
			{Name: "b", Type: schema.IntegerType{}, Default: "7"},
			{Name: "c", Type: schema.TextType{}, Nullable: true},
		},
	})
	return table,
		query.TypedColumnOf[bulkTestRow, string](table.Column("a")),
		query.TypedColumnOf[bulkTestRow, int64](table.Column("b")),
		query.NullableColumnOf[bulkTestRow, *string](table.Column("c"))
}

func TestBulkPlanBatchesByConsecutiveMaskRowsAndActualBinds(t *testing.T) {
	table, a, b, c := bulkTestTable()
	plans := make([]rasql.CreatePlan[bulkTestRow], 0, 4)
	for _, fields := range [][]rasql.MutationField[bulkTestRow]{
		{rasql.SetField(a, "first")},
		{rasql.SetField(a, "second")},
		{rasql.SetField(b, 3), rasql.SetNullableField(c, pointer("third"))},
		{rasql.SetField(a, "fourth")},
	} {
		plan, err := rasql.NewCreatePlan(table, fields...)
		require.NoError(t, err)
		plans = append(plans, plan)
	}
	bulk, err := rasql.NewBulkPlan(plans...)
	require.NoError(t, err)
	handle := &bulkRecordingHandle{}
	db, err := rasql.New(handle, dialect.SQLite())
	require.NoError(t, err)
	outcome, err := rasql.ExecBulkCreate(t.Context(), db, bulk, rasql.BulkOptions{MaxRows: 2, MaxBindParameters: 3})
	require.NoError(t, err)
	require.Equal(t, []rasql.InputRange{{First: 0, Last: 3}}, outcome.Completed)
	require.True(t, outcome.Durable)
	require.Len(t, handle.calls, 3)
	require.Len(t, handle.calls[0].args, 2)
	require.Len(t, handle.calls[1].args, 2)
	require.Len(t, handle.calls[2].args, 1)
	for _, call := range handle.calls {
		require.LessOrEqual(t, len(call.args), 3)
	}
}

func TestBulkPlanDefaultOnlyRowsRemainIndividualAndLimitsValidateBeforeExecution(t *testing.T) {
	table, a, b, _ := bulkTestTable()
	first, err := rasql.NewCreatePlan(table, rasql.DefaultField(a), rasql.DefaultField(b))
	require.NoError(t, err)
	second, err := rasql.NewCreatePlan(table, rasql.DefaultField(a), rasql.DefaultField(b))
	require.NoError(t, err)
	bulk, err := rasql.NewBulkPlan(first, second)
	require.NoError(t, err)
	handle := &bulkRecordingHandle{}
	db, err := rasql.New(handle, dialect.SQLite())
	require.NoError(t, err)
	outcome, err := rasql.ExecBulkCreate(t.Context(), db, bulk, rasql.BulkOptions{MaxRows: 100, MaxBindParameters: 1})
	require.NoError(t, err)
	require.Equal(t, []rasql.InputRange{{First: 0, Last: 1}}, outcome.Completed)
	require.Len(t, handle.calls, 2)
	for _, call := range handle.calls {
		require.Empty(t, call.args)
		require.Contains(t, call.sql, "DEFAULT VALUES")
	}
	_, err = rasql.ExecBulkCreate(t.Context(), db, bulk, rasql.BulkOptions{MaxRows: -1})
	require.Error(t, err)
	require.Len(t, handle.calls, 2)
}

type fixedBulkClassifier struct{ certainty rasql.FailureCertainty }

func (c fixedBulkClassifier) Certainty(error) rasql.FailureCertainty { return c.certainty }

func TestBulkPlanStopsAtFailedBatchAndKeepsOriginalIndexes(t *testing.T) {
	table, a, b, _ := bulkTestTable()
	plans := make([]rasql.CreatePlan[bulkTestRow], 0, 3)
	for _, fields := range [][]rasql.MutationField[bulkTestRow]{
		{rasql.SetField(a, "first")},
		{rasql.SetField(a, "second")},
		{rasql.SetField(b, 3)},
	} {
		plan, err := rasql.NewCreatePlan(table, fields...)
		require.NoError(t, err)
		plans = append(plans, plan)
	}
	bulk, err := rasql.NewBulkPlan(plans...)
	require.NoError(t, err)
	original := fmt.Errorf("driver rejected batch")
	handle := &bulkFailHandle{failAt: 2, err: original}
	db, err := rasql.New(handle, dialect.SQLite())
	require.NoError(t, err)
	outcome, err := rasql.ExecBulkCreate(t.Context(), db, bulk, rasql.BulkOptions{
		MaxRows: 2, MaxBindParameters: 3, Classifier: fixedBulkClassifier{certainty: rasql.OutcomeRejected},
	})
	require.ErrorIs(t, err, original)
	require.Equal(t, []rasql.InputRange{{First: 0, Last: 1}}, outcome.Completed)
	require.Equal(t, []int{2}, outcome.Failed.Indexes)
	require.Equal(t, rasql.OutcomeRejected, outcome.Failed.Certainty)
	require.Len(t, handle.calls, 2)
}

func pointer(value string) *string { return &value }
