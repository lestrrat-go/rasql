package rasql_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dberror"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
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

type zeroLimitDialect struct{ dialect.Dialect }

func (zeroLimitDialect) Name() string { return "custom" }

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
	require.Equal(t, []any{"first", "second"}, handle.calls[0].args)
	require.Equal(t, []any{int64(3), pointer("third")}, handle.calls[1].args)
	require.Equal(t, []any{"fourth"}, handle.calls[2].args)
}

func TestBulkPlanDefaultLimitsAndInvalidLimitsMakeNoCalls(t *testing.T) {
	table, a, _, _ := bulkTestTable()
	plan, err := rasql.NewCreatePlan(table, rasql.SetField(a, "value"))
	require.NoError(t, err)
	bulk, err := rasql.NewBulkPlan(plan)
	require.NoError(t, err)
	for _, test := range []struct {
		dialect dialect.Dialect
		binds   int
	}{
		{dialect.PostgreSQL(), 65535},
		{dialect.MySQL(), 65535},
		{dialect.SQLite(), 32766},
	} {
		handle := &bulkRecordingHandle{}
		db, dbErr := rasql.New(handle, test.dialect)
		require.NoError(t, dbErr)
		outcome, execErr := rasql.ExecBulkCreate(t.Context(), db, bulk, rasql.BulkOptions{})
		require.NoError(t, execErr)
		require.Equal(t, []rasql.InputRange{{First: 0, Last: 0}}, outcome.Completed)
		require.Len(t, handle.calls, 1)
		rowPlans := make([]rasql.CreatePlan[bulkTestRow], 1001)
		for i := range rowPlans {
			rowPlans[i], err = rasql.NewCreatePlan(table, rasql.SetField(a, fmt.Sprintf("row-%d", i)))
			require.NoError(t, err)
		}
		rowBulk, err := rasql.NewBulkPlan(rowPlans...)
		require.NoError(t, err)
		handle.calls = nil
		_, err = rasql.ExecBulkCreate(t.Context(), db, rowBulk, rasql.BulkOptions{MaxBindParameters: test.binds})
		require.NoError(t, err)
		require.Len(t, handle.calls, 2)
		bindPlans := make([]rasql.CreatePlan[bulkTestRow], test.binds+1)
		for i := range bindPlans {
			bindPlans[i], err = rasql.NewCreatePlan(table, rasql.SetField(a, fmt.Sprintf("bind-%d", i)))
			require.NoError(t, err)
		}
		bindBulk, err := rasql.NewBulkPlan(bindPlans...)
		require.NoError(t, err)
		handle.calls = nil
		_, err = rasql.ExecBulkCreate(t.Context(), db, bindBulk, rasql.BulkOptions{MaxRows: test.binds + 1})
		require.NoError(t, err)
		require.Len(t, handle.calls, 2)
	}
	for _, options := range []rasql.BulkOptions{{MaxRows: -1}, {MaxBindParameters: -1}} {
		handle := &bulkRecordingHandle{}
		db, dbErr := rasql.New(handle, dialect.SQLite())
		require.NoError(t, dbErr)
		_, execErr := rasql.ExecBulkCreate(t.Context(), db, bulk, options)
		require.Error(t, execErr)
		require.Empty(t, handle.calls)
	}
	defaults, err := rasql.NewCreatePlan(table, rasql.DefaultField(a))
	require.NoError(t, err)
	defaultBulk, err := rasql.NewBulkPlan(defaults)
	require.NoError(t, err)
	handle := &bulkRecordingHandle{}
	db, err := rasql.New(handle, zeroLimitDialect{Dialect: dialect.SQLite()})
	require.NoError(t, err)
	_, err = rasql.ExecBulkCreate(t.Context(), db, defaultBulk, rasql.BulkOptions{})
	require.Error(t, err)
	require.Empty(t, handle.calls)
}

func TestBulkPlanSplitsAtRowAndActualBindBoundaries(t *testing.T) {
	table, a, _, c := bulkTestTable()
	plans := make([]rasql.CreatePlan[bulkTestRow], 0, 4)
	for _, value := range []string{"one", "two", "three", "four"} {
		plan, err := rasql.NewCreatePlan(table, rasql.SetField(a, value), rasql.ClearField(c))
		require.NoError(t, err)
		plans = append(plans, plan)
	}
	bulk, err := rasql.NewBulkPlan(plans...)
	require.NoError(t, err)
	handle := &bulkRecordingHandle{}
	db, err := rasql.New(handle, dialect.SQLite())
	require.NoError(t, err)
	_, err = rasql.ExecBulkCreate(t.Context(), db, bulk, rasql.BulkOptions{MaxRows: 2, MaxBindParameters: 10})
	require.NoError(t, err)
	require.Len(t, handle.calls, 2)
	require.Equal(t, []any{"one", nil, "two", nil}, handle.calls[0].args)
	require.Equal(t, []any{"three", nil, "four", nil}, handle.calls[1].args)
	handle.calls = nil
	_, err = rasql.ExecBulkCreate(t.Context(), db, bulk, rasql.BulkOptions{MaxRows: 100, MaxBindParameters: 3})
	require.NoError(t, err)
	require.Len(t, handle.calls, 4)
	for i, value := range []string{"one", "two", "three", "four"} {
		require.Equal(t, []any{value, nil}, handle.calls[i].args)
	}
}

func TestBulkPlanPreservesDuplicateFailureIndexesAndInputIsolation(t *testing.T) {
	table, a, _, _ := bulkTestTable()
	fields := []rasql.MutationField[bulkTestRow]{rasql.SetField(a, "original")}
	plan, err := rasql.NewCreatePlan(table, fields...)
	require.NoError(t, err)
	fields[0] = rasql.SetField(a, "mutated")
	plans := []rasql.CreatePlan[bulkTestRow]{plan, plan}
	bulk, err := rasql.NewBulkPlan(plans...)
	require.NoError(t, err)
	plans[0] = rasql.CreatePlan[bulkTestRow]{}
	handle := &bulkFailHandle{failAt: 1, err: fmt.Errorf("rejected")}
	db, err := rasql.New(handle, dialect.SQLite())
	require.NoError(t, err)
	outcome, err := rasql.ExecBulkCreate(t.Context(), db, bulk, rasql.BulkOptions{
		Classifier: fixedBulkClassifier{certainty: rasql.OutcomeRejected},
	})
	require.Error(t, err)
	require.Equal(t, []int{0, 1}, outcome.Failed.Indexes)
	require.Equal(t, []any{"original", "original"}, handle.calls[0].args)
}

func TestBulkPlanRejectsEmptyInput(t *testing.T) {
	_, err := rasql.NewBulkPlan[bulkTestRow]()
	require.Error(t, err)
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

func TestBulkCallerTransactionCommitRemainsNonDurable(t *testing.T) {
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
	plan, err := rasql.NewCreatePlan(table, rasql.SetField(a, "caller-commit"))
	require.NoError(t, err)
	bulk, err := rasql.NewBulkPlan(plan)
	require.NoError(t, err)
	tx, err := sqlDB.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	db, err := rasql.New(tx, dialect.SQLite())
	require.NoError(t, err)
	outcome, err := rasql.ExecBulkCreate(t.Context(), db, bulk, rasql.BulkOptions{})
	require.NoError(t, err)
	require.False(t, outcome.Durable)
	require.NoError(t, tx.Commit())
	var count int
	require.NoError(t, sqlDB.QueryRow(`SELECT count(*) FROM items WHERE a = 'caller-commit'`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestBulkCallerSavepointPreservesOuterSentinel(t *testing.T) {
	sqlDB, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()
	sqlDB.SetMaxOpenConns(1)
	_, err = sqlDB.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY AUTOINCREMENT, a TEXT NOT NULL UNIQUE)`)
	require.NoError(t, err)
	table := rasql.MustTableOf[bulkTestRow](schema.TableDef{
		Name: "items", PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}, Identity: schema.IdentityAlways}, {Name: "a", Type: schema.TextType{}}},
	})
	a := query.TypedColumnOf[bulkTestRow, string](table.Column("a"))
	first, err := rasql.NewCreatePlan(table, rasql.SetField(a, "nested"))
	require.NoError(t, err)
	second, err := rasql.NewCreatePlan(table, rasql.SetField(a, "sentinel"))
	require.NoError(t, err)
	bulk, err := rasql.NewBulkPlan(first, second)
	require.NoError(t, err)
	tx, err := sqlDB.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	_, err = tx.Exec(`INSERT INTO items (a) VALUES ('sentinel')`)
	require.NoError(t, err)
	db, err := rasql.New(tx, dialect.SQLite())
	require.NoError(t, err)
	outcome, err := rasql.ExecBulkCreate(t.Context(), db, bulk, rasql.BulkOptions{
		Atomic: true, Classifier: fixedBulkClassifier{certainty: rasql.OutcomeRejected},
	})
	require.Error(t, err)
	require.False(t, outcome.Durable)
	var nested, sentinel int
	require.NoError(t, tx.QueryRow(`SELECT count(*) FROM items WHERE a = 'nested'`).Scan(&nested))
	require.NoError(t, tx.QueryRow(`SELECT count(*) FROM items WHERE a = 'sentinel'`).Scan(&sentinel))
	require.Equal(t, 0, nested)
	require.Equal(t, 1, sentinel)
	require.NoError(t, tx.Rollback())
	require.NoError(t, sqlDB.QueryRow(`SELECT count(*) FROM items WHERE a = 'sentinel'`).Scan(&sentinel))
	require.Equal(t, 0, sentinel)
}

func TestGeneratedCreateBuildersRunThroughBulkSQLite(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)
	_, err = database.Exec(`CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT NOT NULL,
		nickname TEXT,
		status TEXT NOT NULL DEFAULT 'pending',
		first_name TEXT NOT NULL DEFAULT '',
		last_name TEXT NOT NULL DEFAULT ''
	)`)
	require.NoError(t, err)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	plans := []rasql.CreatePlan[store.UsersRow]{
		store.NewUsersCreate().Email("one@example.com").FirstName("One").LastName("User").Plan(),
		store.NewUsersCreate().Email("two@example.com").ClearNickname().FirstName("Two").LastName("User").Plan(),
		store.NewUsersCreate().Email("three@example.com").Status("").FirstName("Three").LastName("User").Plan(),
		store.NewUsersCreate().Email("four@example.com").DefaultStatus().FirstName("Four").LastName("User").Plan(),
	}
	bulk, err := rasql.NewBulkPlan(plans...)
	require.NoError(t, err)
	outcome, err := rasql.ExecBulkCreate(t.Context(), db, bulk, rasql.BulkOptions{MaxRows: 2, MaxBindParameters: 6})
	require.NoError(t, err)
	require.Equal(t, []rasql.InputRange{{First: 0, Last: 3}}, outcome.Completed)
	rows, err := database.Query(`SELECT email, nickname, status FROM users ORDER BY id`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var got []struct {
		email, status string
		nickname      *string
	}
	for rows.Next() {
		var row struct {
			email, status string
			nickname      *string
		}
		require.NoError(t, rows.Scan(&row.email, &row.nickname, &row.status))
		got = append(got, row)
	}
	require.NoError(t, rows.Err())
	require.Len(t, got, 4)
	require.Equal(t, "pending", got[0].status)
	require.Nil(t, got[1].nickname)
	require.Equal(t, "", got[2].status)
	require.Equal(t, "pending", got[3].status)
}

func TestBulkPlanRejectsInvalidInputsBeforeHandleCalls(t *testing.T) {
	table, a, b, _ := bulkTestTable()
	invalid, constructorErr := rasql.NewCreatePlan(table)
	require.Error(t, constructorErr)
	_, err := rasql.NewBulkPlan(invalid)
	require.ErrorIs(t, err, constructorErr)
	other := rasql.MustTableOf[bulkTestRow](schema.TableDef{Name: "other", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	otherA := query.TypedColumnOf[bulkTestRow, int64](other.Column("id"))
	otherPlan, err := rasql.NewCreatePlan(other, rasql.SetField(otherA, 1))
	require.NoError(t, err)
	valid, err := rasql.NewCreatePlan(table, rasql.SetField(a, "valid"))
	require.NoError(t, err)
	_, err = rasql.NewBulkPlan(valid, otherPlan)
	require.ErrorContains(t, err, "mixed tables")
	wide, err := rasql.NewCreatePlan(table, rasql.SetField(a, "wide"), rasql.SetField(b, 1))
	require.NoError(t, err)
	bulk, err := rasql.NewBulkPlan(wide)
	require.NoError(t, err)
	handle := &bulkRecordingHandle{}
	db, err := rasql.New(handle, dialect.SQLite())
	require.NoError(t, err)
	_, err = rasql.ExecBulkCreate(t.Context(), db, bulk, rasql.BulkOptions{MaxBindParameters: 1})
	require.ErrorContains(t, err, "uses 2 bind parameters")
	require.Empty(t, handle.calls)
	_, err = rasql.ExecBulkCreate(t.Context(), db, bulk, rasql.BulkOptions{MaxRows: -1})
	require.Error(t, err)
	require.Empty(t, handle.calls)
}

func TestBulkOwnedRollbackFailureMarksAttemptedIndexesUnknown(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = database.Close() }()
	table, a, _, _ := bulkTestTable()
	first, err := rasql.NewCreatePlan(table, rasql.SetField(a, "first"))
	require.NoError(t, err)
	second, err := rasql.NewCreatePlan(table, rasql.SetField(a, "second"))
	require.NoError(t, err)
	bulk, err := rasql.NewBulkPlan(first, second)
	require.NoError(t, err)
	statementErr := fmt.Errorf("statement failed")
	rollbackErr := fmt.Errorf("rollback failed")
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO").WillReturnError(statementErr)
	mock.ExpectRollback().WillReturnError(rollbackErr)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	outcome, err := rasql.ExecBulkCreate(t.Context(), db, bulk, rasql.BulkOptions{MaxRows: 1, Atomic: true})
	require.ErrorIs(t, err, statementErr)
	require.ErrorIs(t, err, rollbackErr)
	require.Equal(t, []int{0, 1}, outcome.Failed.Indexes)
	require.Equal(t, rasql.OutcomeUnknown, outcome.Failed.Certainty)
	require.False(t, outcome.Durable)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBulkOwnedCommitFailureMarksAllIndexesUnknown(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = database.Close() }()
	table, a, _, _ := bulkTestTable()
	plan, err := rasql.NewCreatePlan(table, rasql.SetField(a, "one"))
	require.NoError(t, err)
	bulk, err := rasql.NewBulkPlan(plan)
	require.NoError(t, err)
	commitErr := fmt.Errorf("commit failed")
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit().WillReturnError(commitErr)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	outcome, err := rasql.ExecBulkCreate(t.Context(), db, bulk, rasql.BulkOptions{Atomic: true})
	require.ErrorIs(t, err, commitErr)
	require.Equal(t, []int{0}, outcome.Failed.Indexes)
	require.Equal(t, rasql.OutcomeUnknown, outcome.Failed.Certainty)
	require.False(t, outcome.Durable)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBulkBeginFailureAttemptsNoInput(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = database.Close() }()
	table, a, _, _ := bulkTestTable()
	plan, err := rasql.NewCreatePlan(table, rasql.SetField(a, "one"))
	require.NoError(t, err)
	bulk, err := rasql.NewBulkPlan(plan)
	require.NoError(t, err)
	beginErr := fmt.Errorf("begin failed")
	mock.ExpectBegin().WillReturnError(beginErr)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	outcome, err := rasql.ExecBulkCreate(t.Context(), db, bulk, rasql.BulkOptions{Atomic: true})
	require.ErrorIs(t, err, beginErr)
	require.Empty(t, outcome.Completed)
	require.Nil(t, outcome.Failed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBulkCallerSavepointCleanupFailureMarksAttemptedIndexesUnknown(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = database.Close() }()
	mock.ExpectBegin()
	tx, err := database.Begin()
	require.NoError(t, err)
	table, a, _, _ := bulkTestTable()
	plan, err := rasql.NewCreatePlan(table, rasql.SetField(a, "one"))
	require.NoError(t, err)
	bulk, err := rasql.NewBulkPlan(plan)
	require.NoError(t, err)
	statementErr := fmt.Errorf("statement failed")
	rollbackErr := fmt.Errorf("savepoint rollback failed")
	releaseErr := fmt.Errorf("savepoint release failed")
	mock.ExpectExec("SAVEPOINT").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO").WillReturnError(statementErr)
	mock.ExpectExec("ROLLBACK TO SAVEPOINT").WillReturnError(rollbackErr)
	mock.ExpectExec("RELEASE SAVEPOINT").WillReturnError(releaseErr)
	db, err := rasql.New(tx, dialect.SQLite())
	require.NoError(t, err)
	outcome, err := rasql.ExecBulkCreate(t.Context(), db, bulk, rasql.BulkOptions{Atomic: true})
	require.ErrorIs(t, err, statementErr)
	require.ErrorIs(t, err, rollbackErr)
	require.ErrorIs(t, err, releaseErr)
	require.Equal(t, []int{0}, outcome.Failed.Indexes)
	require.Equal(t, rasql.OutcomeUnknown, outcome.Failed.Certainty)
	require.False(t, outcome.Durable)
	_ = tx.Rollback()
}

func TestBulkCallerSavepointSuccessPreservesRejectedClassification(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = database.Close() }()
	mock.ExpectBegin()
	tx, err := database.Begin()
	require.NoError(t, err)
	table, a, _, _ := bulkTestTable()
	plan, err := rasql.NewCreatePlan(table, rasql.SetField(a, "one"))
	require.NoError(t, err)
	bulk, err := rasql.NewBulkPlan(plan)
	require.NoError(t, err)
	statementErr := fmt.Errorf("rejected statement")
	mock.ExpectExec("SAVEPOINT").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO").WillReturnError(statementErr)
	mock.ExpectExec("ROLLBACK TO SAVEPOINT").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("RELEASE SAVEPOINT").WillReturnResult(sqlmock.NewResult(0, 0))
	db, err := rasql.New(tx, dialect.SQLite())
	require.NoError(t, err)
	outcome, err := rasql.ExecBulkCreate(t.Context(), db, bulk, rasql.BulkOptions{
		Atomic: true, Classifier: fixedBulkClassifier{certainty: rasql.OutcomeRejected},
	})
	require.ErrorIs(t, err, statementErr)
	require.Equal(t, []int{0}, outcome.Failed.Indexes)
	require.Equal(t, rasql.OutcomeRejected, outcome.Failed.Certainty)
	require.False(t, outcome.Durable)
	require.NoError(t, mock.ExpectationsWereMet())
	_ = tx.Rollback()
}

func pointer(value string) *string { return &value }
