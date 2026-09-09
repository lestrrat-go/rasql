package schemagen

import (
	"reflect"
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestResolvedNamesKeepExistingEntriesStableWhenAppendingColumn(t *testing.T) {
	beforeTable := schema.MustTableDef("users", schema.Integer("id"), schema.Text("display_name"))
	before, err := ResolveNames("generated", []schema.TableDef{beforeTable}, NameOverrides{})
	require.NoError(t, err)
	afterTable := beforeTable.Clone()
	afterTable.Columns = append(afterTable.Columns, schema.ColumnDef{Name: "unrelated", Type: schema.TextType{}})
	after, err := ResolveNames("generated", []schema.TableDef{afterTable}, NameOverrides{})
	require.NoError(t, err)
	beforeObject, _ := before.Object(beforeTable)
	afterObject, _ := after.Object(afterTable)
	require.Equal(t, beforeObject, afterObject)
	for _, column := range beforeTable.Columns {
		beforeColumn, _ := before.Column(beforeTable, column.Name)
		afterColumn, _ := after.Column(afterTable, column.Name)
		require.True(t, reflect.DeepEqual(beforeColumn, afterColumn), column.Name)
	}
	require.Equal(t, before.Filename(beforeTable), after.Filename(afterTable))
}

func TestResolveNamesUsesPhysicalIdentityAndClonesOverrides(t *testing.T) {
	mainTable := schema.MustTableDef("customer-id", schema.Integer("id"), schema.Text("foo_bar"))
	mainTable.Schema = "main"
	auditTable := schema.MustTableDef("customer-id", schema.Integer("id"), schema.Text("foo_bar"))
	auditTable.Schema = "audit"
	columns := map[string]ColumnNameOverrides{"foo_bar": {Field: "MainValue", Accessor: "MainValueColumn"}}
	overrides := NameOverrides{Objects: map[schema.ObjectName]ObjectNameOverrides{{Schema: "main", Name: "customer-id"}: {Accessor: "MainCustomer", TableType: "MainTable", RowType: "MainRow", FileBase: "main_customer", Columns: columns}, {Schema: "audit", Name: "customer-id"}: {Accessor: "AuditCustomer"}}}
	names, err := ResolveNames("generated", []schema.TableDef{auditTable, mainTable}, overrides)
	require.NoError(t, err)
	columns["foo_bar"] = ColumnNameOverrides{Field: "Changed", Accessor: "Changed"}
	main, ok := names.Object(mainTable)
	require.True(t, ok)
	require.Equal(t, "MainCustomer", main.Accessor)
	require.Equal(t, "main_customer_gen.go", names.Filename(mainTable))
	column, ok := names.Column(mainTable, "foo_bar")
	require.True(t, ok)
	require.Equal(t, "MainValue", column.Field)
	audit, ok := names.Object(auditTable)
	require.True(t, ok)
	require.Equal(t, "AuditCustomer", audit.Accessor)
	_, ok = names.Object(schema.TableDef{Name: "customer-id"})
	require.False(t, ok)
	dot := schema.MustTableDef("literal.dot", schema.Text("Case"), schema.Text("case"))
	dot.Schema = "Main"
	resolved, err := ResolveNames("generated", []schema.TableDef{dot}, NameOverrides{Objects: map[schema.ObjectName]ObjectNameOverrides{{Schema: "Main", Name: "literal.dot"}: {Accessor: "LiteralDot", Columns: map[string]ColumnNameOverrides{"Case": {Field: "UpperCase", Accessor: "UpperCaseColumn"}, "case": {Field: "LowerCase", Accessor: "LowerCaseColumn"}}}}})
	require.NoError(t, err)
	_, ok = resolved.Object(schema.TableDef{Schema: "main", Name: "literal.dot"})
	require.False(t, ok)
	_, ok = resolved.Column(dot, "Case")
	require.True(t, ok)
	_, ok = resolved.Column(dot, "case")
	require.True(t, ok)
}

func TestResolveNamesRejectsUnknownPhysicalColumn(t *testing.T) {
	table := schema.MustTableDef("users", schema.Integer("id"))
	_, err := ResolveNames("generated", []schema.TableDef{table}, NameOverrides{Objects: map[schema.ObjectName]ObjectNameOverrides{{Name: "users"}: {Columns: map[string]ColumnNameOverrides{"missing": {Field: "Missing"}}}}})
	require.ErrorContains(t, err, "unknown column")
}

func TestResolveNamesRejectsFinalDeclarationCollisions(t *testing.T) {
	table := schema.MustTableDef("users", schema.Integer("id"), schema.Text("name"))
	_, err := ResolveNames("generated", []schema.TableDef{table}, NameOverrides{Objects: map[schema.ObjectName]ObjectNameOverrides{{Name: "users"}: {Columns: map[string]ColumnNameOverrides{"id": {Accessor: "Name"}}}}})
	require.ErrorContains(t, err, "final accessor")
	_, err = ResolveNames("generated", []schema.TableDef{table}, NameOverrides{Objects: map[schema.ObjectName]ObjectNameOverrides{{Name: "users"}: {Accessor: "Tables"}}})
	require.ErrorContains(t, err, "Tables")
}
