package rasql

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestExecMutationPreservesRowsAffectedAfterHookFailure pins the third
// defect: ExecMutation discarded executor.Exec's sql.Result whenever it
// returned a non-nil error at all, even though *ExtensionError reports that
// the driver call underneath it succeeded. A hook failing after a write that
// really happened should still let the caller learn how many rows it
// affected, the same way exec.DB.ExecRendered already hands the result back
// alongside a joined hook error.
//
// Confirmed by running this test against the unmodified ExecMutation: it
// returned MutationOutcome{Durability: DurabilityUnknown} (Affected: 0)
// alongside the hook error, instead of the affected row count below.
func TestExecMutationPreservesRowsAffectedAfterHookFailure(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	hookErr := errors.New("export failed")
	hook := HookFunc{AfterFunc: func(context.Context, Operation, error) error { return hookErr }}
	db, err := New(database, dialect.SQLite(), hook)
	require.NoError(t, err)

	table, err := query.NewTableRef(schema.TableDef{
		Name:    "users",
		Columns: []schema.ColumnDef{{Name: "email", Type: schema.TextType{}}},
	})
	require.NoError(t, err)
	insert, err := query.NewInsert(table, query.Set(table.Column("email"), "ada@example.com"))
	require.NoError(t, err)
	mock.ExpectExec("INSERT INTO").WithArgs("ada@example.com").WillReturnResult(sqlmock.NewResult(1, 1))

	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	plan, err := NewStatementPlan(insert)
	require.NoError(t, err)

	outcome, err := ExecMutation(t.Context(), executor, plan)
	require.Error(t, err)
	var extensionErr *ExtensionError
	require.ErrorAs(t, err, &extensionErr)
	require.True(t, extensionErr.ExecutionSucceeded())
	require.Equal(t, int64(1), outcome.Affected)
	require.Equal(t, DurabilityCommitted, outcome.Durability)
}
