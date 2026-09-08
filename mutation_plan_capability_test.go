package rasql

import (
	"testing"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type capabilityGuardRow struct {
	ID int64 `rasql:"id"`
}

// capabilityGuardTable is a Table[T] TableOf and MustTableOf would both
// refuse: it describes a read-only view. TableFrom is the one entry point
// that skips that check, which is exactly how a forged handle reaches
// NewCreatePlan or NewPatchPlan without ever passing through TableOf: it is
// the entry point rasqlgen uses for a descriptor it has already validated
// itself, so it validates nothing about the descriptor it is given here.
func capabilityGuardTable(t *testing.T) Table[capabilityGuardRow] {
	t.Helper()
	return TableFrom[capabilityGuardRow](schema.TableDef{
		Name:       "active_users",
		Kind:       schema.ObjectView,
		Operations: schema.OperationRead,
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	})
}

// TestForgedTableHandleRejectsInsertAndUpdate reconstructs the case
// TestForgedWritableHandleRejectsEveryMutation (table_capability_test.go,
// removed from this branch because it also exercised rasql.Insert and
// rasql.Update helpers this redesign does not carry forward) proved for
// every mutation kind: a Table[T] whose schema does not support the
// operation must be refused at the plan constructor, not just at TableOf.
//
// Confirmed by running both subtests against the unmodified constructors:
// neither NewCreatePlan nor NewPatchPlan called requireTableOperation at
// all, so both returned a nil error for a plan built against a read-only
// view, where NewDeletePlan on the same handle already reported
// `rasql: object "active_users" does not support operation 2`.
func TestForgedTableHandleRejectsInsertAndUpdate(t *testing.T) {
	table := capabilityGuardTable(t)
	id := query.TypedColumnOf[capabilityGuardRow, int64](table.Column("id"))

	t.Run("insert", func(t *testing.T) {
		field := SetField[capabilityGuardRow, int64](id, 1)
		_, err := NewCreatePlan(table, field)
		require.ErrorContains(t, err, "does not support operation")
	})

	t.Run("update", func(t *testing.T) {
		field := SetField[capabilityGuardRow, int64](id, 1)
		where := query.EqualValue(id, int64(1))
		_, err := NewPatchPlan(table, where, field)
		require.ErrorContains(t, err, "does not support operation")
	})
}
