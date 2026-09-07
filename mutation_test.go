package rasql_test

import (
	"context"
	"database/sql"
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
	require.Equal(t, rasql.DurabilityUnknown, outcome.Durability)

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
	outcome, err := rasql.ExecMutationBatch(t.Context(), executor, plans, rasql.MutationBatchOptions{MaxRows: 3})
	require.NoError(t, err)
	require.Equal(t, []rasql.InputOutcome{rasql.InputApplied, rasql.InputApplied, rasql.InputApplied, rasql.InputApplied, rasql.InputApplied, rasql.InputApplied}, outcome.Inputs)
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
	outcome, err := rasql.ExecMutationBatch(t.Context(), executor, plans, rasql.MutationBatchOptions{MaxRows: 3, Classifier: mutationRejectClassifier{}})
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
	outcome, err := rasql.ExecMutationBatch(ctx, executor, []rasql.MutationPlan{plan}, rasql.MutationBatchOptions{})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, []rasql.InputOutcome{rasql.InputUnattempted}, outcome.Inputs)
}
