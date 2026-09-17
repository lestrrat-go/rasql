package schemagen_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestPackageSourceGeneratesReadOnlyViewSurface pins the declarations a view
// gets. It holds one rasql.Table like every other object, because a view is a
// table whose descriptor permits only reading, and it gets no mutation builder
// because that descriptor permits neither insert nor update.
func TestPackageSourceGeneratesReadOnlyViewSurface(t *testing.T) {
	text := compactRenderedSource(t, schema.TableDef{
		Name:       "active_users",
		Kind:       schema.ObjectView,
		Operations: schema.OperationRead,
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
		},
	})
	require.Contains(t, text, "rasql.Table[ActiveUsersRow]")
	require.Contains(t, text, "rasql.MustTableOf[ActiveUsersRow]")
	require.Contains(t, text, "ID rasql.Column[ActiveUsersRow, int64]")
	require.NotContains(t, text, "ActiveUsersCreate")
	require.NotContains(t, text, "ActiveUsersPatch")
	require.NotContains(t, text, "activeUsersMutationColumns")
}

// TestPackageSourceSelectsMutationsPerOperation pins that the mutation
// builders follow the descriptor's own Operations rather than its Kind. A
// descriptor naming read and insert gets a create builder and no patch
// builder, which requireWritableDefinition used to make unreachable by
// refusing the descriptor outright.
func TestPackageSourceSelectsMutationsPerOperation(t *testing.T) {
	appendOnly := schema.TableDef{
		Name:       "audit_log",
		Operations: schema.OperationRead | schema.OperationInsert,
		PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "action", Type: schema.TextType{}},
		},
	}
	text := compactRenderedSource(t, appendOnly)
	require.Contains(t, text, "type AuditLogCreate struct")
	require.Contains(t, text, "func (v AuditLogCreate) Plan() (rasql.CreatePlan[AuditLogRow], error)")
	require.NotContains(t, text, "type AuditLogPatch struct")

	updateOnly := appendOnly
	updateOnly.Operations = schema.OperationRead | schema.OperationUpdate
	text = compactRenderedSource(t, updateOnly)
	require.NotContains(t, text, "type AuditLogCreate struct")
	require.Contains(t, text, "type AuditLogPatch struct")
	require.Contains(t, text, "func (v AuditLogPatch) Where(value rasql.Predicate) (rasql.PatchPlan[AuditLogRow], error)")
}
