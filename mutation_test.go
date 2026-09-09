package rasql_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type mutationRow struct{ ID int64 }

type mutationDecoder struct{ schema rasql.ResultSchema }

type mutationRejectClassifier struct{}

func (mutationRejectClassifier) Certainty(error) rasql.FailureCertainty { return rasql.OutcomeRejected }

func (d mutationDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (d mutationDecoder) Presence() []rasql.Presence       { return nil }
func (d mutationDecoder) DecodeRow(source rasql.ScanSource, row *mutationRow) error {
	return source.Scan(&row.ID)
}

func mutationFixture(t *testing.T) (rasql.Executor, rasql.Table[mutationRow], query.TypedColumn[mutationRow, int64]) {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.ExecContext(t.Context(), `CREATE TABLE items (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`)
	require.NoError(t, err)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `ALTER TABLE items ADD COLUMN version INTEGER NOT NULL DEFAULT 1`)
	require.NoError(t, err)
	table, err := rasql.TableOf[mutationRow](schema.TableDef{
		Name: "items", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "value", Type: schema.TextType{}}, {Name: "version", Type: schema.IntegerType{}, Default: "1"}}, PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 40, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	return executor, table, query.TypedColumnOf[mutationRow, int64](table.Column("id"))
}

func TestMutationOptimisticVersion(t *testing.T) {
	executor, table, id := mutationFixture(t)
	value := query.TypedColumnOf[mutationRow, string](table.Column("value"))
	relation, err := rasql.SourceOf[mutationRow](table, "")
	require.NoError(t, err)
	version, err := rasql.BindColumn[mutationRow, int64](relation, "version", "")
	require.NoError(t, err)
	create, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(10)), rasql.SetField(value, "before"))
	require.NoError(t, err)
	_, err = rasql.ExecMutation(t.Context(), executor, create)
	require.NoError(t, err)
	patch, err := rasql.NewPatchPlan(table, query.EqualValue(id, int64(10)), rasql.SetField(value, "after"))
	require.NoError(t, err)
	versioned, err := patch.WithVersion(version, 1)
	require.NoError(t, err)
	outcome, err := rasql.ExecMutation(t.Context(), executor, versioned)
	require.NoError(t, err)
	require.Equal(t, int64(1), outcome.Affected)
	outcome, err = rasql.ExecMutation(t.Context(), executor, versioned)
	require.ErrorIs(t, err, rasql.ErrPrecondition)
	require.Zero(t, outcome.Affected)
}

func mutationProjection(t *testing.T, table rasql.Table[mutationRow]) rasql.Projection[mutationRow] {
	t.Helper()
	schemaValue, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	returnValue := mutationDecoder{schema: schemaValue}
	relation, err := rasql.SourceOf[mutationRow](table, "")
	require.NoError(t, err)
	id, err := rasql.BindColumn[mutationRow, int64](relation, "id", "")
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", id.Expr(), schema.IntegerType{}, "")}, returnValue)
	require.NoError(t, err)
	return projection
}

func TestMutationReturningAndOutcomes(t *testing.T) {
	executor, table, id := mutationFixture(t)
	value := query.TypedColumnOf[mutationRow, string](table.Column("value"))
	create, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(7)), rasql.SetField(value, "before"))
	require.NoError(t, err)
	outcome, err := rasql.ExecMutation(t.Context(), executor, create)
	require.NoError(t, err)
	require.Equal(t, int64(1), outcome.Affected)
	require.Equal(t, rasql.DurabilityCommitted, outcome.Durability)

	projection := mutationProjection(t, table)
	patch, err := rasql.NewPatchPlan(table, query.EqualValue(id, int64(7)), rasql.SetField(value, "after"))
	require.NoError(t, err)
	returned, err := rasql.Returning(patch, projection)
	require.NoError(t, err)
	rows, err := rasql.All(t.Context(), executor, returned)
	require.NoError(t, err)
	require.Equal(t, []mutationRow{{ID: 7}}, rows)

	deletePlan, err := rasql.NewDeletePlan(table, query.EqualValue(id, int64(7)))
	require.NoError(t, err)
	deleteOutcome, err := rasql.ExecMutation(t.Context(), executor, deletePlan)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleteOutcome.Affected)
}

func TestMutationBatchGroupsCompatibleCreates(t *testing.T) {
	executor, table, id := mutationFixture(t)
	value := query.TypedColumnOf[mutationRow, string](table.Column("value"))
	plans := make([]rasql.MutationPlan, 0, 6)
	for i := int64(1); i <= 6; i++ {
		plan, err := rasql.NewCreatePlan(table, rasql.SetField(id, i), rasql.SetField(value, "v"))
		require.NoError(t, err)
		plans = append(plans, plan)
	}
	outcome, err := rasql.ExecMutationBatch(t.Context(), executor, plans, rasql.BulkOptions{MaxRows: 3})
	require.NoError(t, err)
	require.Equal(t, []rasql.InputOutcome{rasql.InputApplied, rasql.InputApplied, rasql.InputApplied, rasql.InputApplied, rasql.InputApplied, rasql.InputApplied}, outcome.Inputs)
}

func TestMutationBatchEmitsLogicalInvocation(t *testing.T) {
	executor, table, id := mutationFixture(t)
	value := query.TypedColumnOf[mutationRow, string](table.Column("value"))
	plans := make([]rasql.MutationPlan, 0, 6)
	for i := int64(1); i <= 6; i++ {
		plan, err := rasql.NewCreatePlan(table, rasql.SetField(id, i), rasql.SetField(value, "event"))
		require.NoError(t, err)
		plans = append(plans, plan)
	}
	var mu sync.Mutex
	var events []rasql.Event
	executor, err := rasql.WithEventObservers(executor, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		return ctx, rasql.EventCompletionFunc(func(_ context.Context, terminal rasql.Event) error {
			mu.Lock()
			events = append(events, terminal)
			mu.Unlock()
			return nil
		})
	}))
	require.NoError(t, err)
	_, err = rasql.ExecMutationBatch(t.Context(), executor, plans, rasql.BulkOptions{MaxRows: 3})
	require.NoError(t, err)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, events, 6)
	require.Equal(t, rasql.EventMutationBatch, events[0].Kind)
	require.Equal(t, rasql.EventStart, events[0].Phase)
	require.Equal(t, rasql.EventStatement, events[1].Kind)
	require.Equal(t, events[0].LogicalID, events[1].ParentID)
	require.Equal(t, 0, events[1].StatementIndex)
	require.Equal(t, rasql.EventStatement, events[3].Kind)
	require.Equal(t, events[0].LogicalID, events[3].ParentID)
	require.Equal(t, 1, events[3].StatementIndex)
	require.Equal(t, rasql.EventMutationBatch, events[5].Kind)
	require.Equal(t, rasql.EventTerminal, events[5].Phase)
}

func TestMutationBatchReportsRejectedBatchAndUnattemptedInputs(t *testing.T) {
	executor, table, id := mutationFixture(t)
	value := query.TypedColumnOf[mutationRow, string](table.Column("value"))
	plans := make([]rasql.MutationPlan, 0, 6)
	for i := int64(1); i <= 6; i++ {
		key := i
		if i == 5 {
			key = 1
		}
		plan, err := rasql.NewCreatePlan(table, rasql.SetField(id, key), rasql.SetField(value, "v"))
		require.NoError(t, err)
		plans = append(plans, plan)
	}
	outcome, err := rasql.ExecMutationBatch(t.Context(), executor, plans, rasql.BulkOptions{MaxRows: 3, Classifier: mutationRejectClassifier{}})
	require.Error(t, err)
	require.Equal(t, []rasql.InputOutcome{
		rasql.InputApplied, rasql.InputApplied, rasql.InputApplied,
		rasql.InputRejected, rasql.InputRejected, rasql.InputRejected,
	}, outcome.Inputs)
	require.Equal(t, []int{3, 4, 5}, outcome.FailedBatch)
}

func TestMutationBatchCancellationBeforeExecution(t *testing.T) {
	executor, table, id := mutationFixture(t)
	value := query.TypedColumnOf[mutationRow, string](table.Column("value"))
	plan, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(1)), rasql.SetField(value, "v"))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	outcome, err := rasql.ExecMutationBatch(ctx, executor, []rasql.MutationPlan{plan}, rasql.BulkOptions{})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, []rasql.InputOutcome{rasql.InputUnattempted}, outcome.Inputs)
}

func TestMutationBatchAtomicRollbackStates(t *testing.T) {
	executor, table, id := mutationFixture(t)
	value := query.TypedColumnOf[mutationRow, string](table.Column("value"))
	first, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(1)), rasql.SetField(value, "one"))
	require.NoError(t, err)
	second, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(1)), rasql.SetField(value, "duplicate"))
	require.NoError(t, err)
	outcome, err := rasql.ExecMutationBatch(t.Context(), executor, []rasql.MutationPlan{first, second}, rasql.BulkOptions{
		MaxRows: 1, Atomic: true, Classifier: mutationRejectClassifier{},
	})
	require.Error(t, err)
	require.Equal(t, []rasql.InputOutcome{rasql.InputRolledBack, rasql.InputRejected}, outcome.Inputs)
	require.Equal(t, rasql.DurabilityPending, outcome.Durability)
}

func TestMutationTransactionDurabilityIsPending(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.ExecContext(t.Context(), `CREATE TABLE items (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`)
	require.NoError(t, err)
	tx, err := database.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	db, err := rasql.New(tx, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 40, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	table, err := rasql.TableOf[mutationRow](schema.TableDef{Name: "items", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "value", Type: schema.TextType{}}}})
	require.NoError(t, err)
	id := query.TypedColumnOf[mutationRow, int64](table.Column("id"))
	value := query.TypedColumnOf[mutationRow, string](table.Column("value"))
	plan, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(1)), rasql.SetField(value, "pending"))
	require.NoError(t, err)
	outcome, err := rasql.ExecMutation(t.Context(), executor, plan)
	require.NoError(t, err)
	require.Equal(t, rasql.DurabilityPending, outcome.Durability)
}
