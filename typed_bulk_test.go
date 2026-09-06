package rasql_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dberror"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
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

type bulkCategoryClassifier struct{ category dberror.Category }

func (c bulkCategoryClassifier) Classify(error) (dberror.Metadata, bool) {
	return dberror.Metadata{Category: c.category}, true
}

func TestConstraintFailureClassifierOnlyRejectsConstraintCategories(t *testing.T) {
	for _, test := range []struct {
		category  dberror.Category
		certainty rasql.FailureCertainty
	}{
		{dberror.UniqueViolation, rasql.OutcomeRejected},
		{dberror.ForeignKeyViolation, rasql.OutcomeRejected},
		{dberror.NotNullViolation, rasql.OutcomeRejected},
		{dberror.CheckViolation, rasql.OutcomeRejected},
		{dberror.TransactionConflict, rasql.OutcomeUnknown},
		{dberror.Unknown, rasql.OutcomeUnknown},
	} {
		classifier := rasql.ConstraintFailureClassifier{Classifiers: []dberror.Classifier{bulkCategoryClassifier{category: test.category}}}
		require.Equal(t, test.certainty, classifier.Certainty(fmt.Errorf("driver error")))
	}
	classifier := rasql.ConstraintFailureClassifier{}
	require.Equal(t, rasql.OutcomeUnknown, classifier.Certainty(nil))
}

func TestBulkAtomicRollbackClearsConfirmedProgress(t *testing.T) {
	sqlDB, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()
	sqlDB.SetMaxOpenConns(1)
	_, err = sqlDB.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY AUTOINCREMENT, a TEXT NOT NULL UNIQUE)`)
	require.NoError(t, err)
	_, err = sqlDB.Exec(`INSERT INTO items (a) VALUES ('duplicate')`)
	require.NoError(t, err)
	table := rasql.MustTableOf[bulkTestRow](schema.TableDef{
		Name: "items", PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}, Identity: schema.IdentityAlways}, {Name: "a", Type: schema.TextType{}}},
	})
	a := query.TypedColumnOf[bulkTestRow, string](table.Column("a"))
	makeBulk := func(first string) rasql.BulkPlan[bulkTestRow] {
		firstPlan, planErr := rasql.NewCreatePlan(table, rasql.SetField(a, first))
		require.NoError(t, planErr)
		secondPlan, planErr := rasql.NewCreatePlan(table, rasql.SetField(a, "duplicate"))
		require.NoError(t, planErr)
		bulk, planErr := rasql.NewBulkPlan(firstPlan, secondPlan)
		require.NoError(t, planErr)
		return bulk
	}
	db, err := rasql.New(sqlDB, dialect.SQLite())
	require.NoError(t, err)
	classifier := rasql.ConstraintFailureClassifier{Classifiers: []dberror.Classifier{bulkCategoryClassifier{category: dberror.UniqueViolation}}}
	outcome, err := rasql.ExecBulkCreate(t.Context(), db, makeBulk("non-atomic"), rasql.BulkOptions{MaxRows: 1, Classifier: classifier})
	require.Error(t, err)
	require.Equal(t, []rasql.InputRange{{First: 0, Last: 0}}, outcome.Completed)
	require.Equal(t, rasql.OutcomeRejected, outcome.Failed.Certainty)
	require.True(t, outcome.Durable)
	var count int
	require.NoError(t, sqlDB.QueryRow(`SELECT count(*) FROM items WHERE a = 'non-atomic'`).Scan(&count))
	require.Equal(t, 1, count)

	outcome, err = rasql.ExecBulkCreate(t.Context(), db, makeBulk("atomic"), rasql.BulkOptions{MaxRows: 1, Atomic: true, Classifier: classifier})
	require.Error(t, err)
	require.Empty(t, outcome.Completed)
	require.Equal(t, rasql.OutcomeRejected, outcome.Failed.Certainty)
	require.False(t, outcome.Durable)
	require.NoError(t, sqlDB.QueryRow(`SELECT count(*) FROM items WHERE a = 'atomic'`).Scan(&count))
	require.Equal(t, 0, count)
}

func TestBulkCallerTransactionIsNotDurable(t *testing.T) {
	sqlDB, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()
	sqlDB.SetMaxOpenConns(1)
	_, err = sqlDB.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY AUTOINCREMENT, a TEXT NOT NULL)`)
	require.NoError(t, err)
	table := rasql.MustTableOf[bulkTestRow](schema.TableDef{
		Name: "items", PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}, Identity: schema.IdentityAlways}, {Name: "a", Type: schema.TextType{}}},
	})
	a := query.TypedColumnOf[bulkTestRow, string](table.Column("a"))
	plan, err := rasql.NewCreatePlan(table, rasql.SetField(a, "caller"))
	require.NoError(t, err)
	bulk, err := rasql.NewBulkPlan(plan)
	require.NoError(t, err)
	tx, err := sqlDB.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	db, err := rasql.New(tx, dialect.SQLite())
	require.NoError(t, err)
	outcome, err := rasql.ExecBulkCreate(t.Context(), db, bulk, rasql.BulkOptions{})
	require.NoError(t, err)
	require.Equal(t, []rasql.InputRange{{First: 0, Last: 0}}, outcome.Completed)
	require.False(t, outcome.Durable)
	require.NoError(t, tx.Rollback())
	var count int
	require.NoError(t, sqlDB.QueryRow(`SELECT count(*) FROM items WHERE a = 'caller'`).Scan(&count))
	require.Zero(t, count)
}

func pointer(value string) *string { return &value }
