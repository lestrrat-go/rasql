package schemagen

import (
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

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
}

func TestResolveNamesRejectsUnknownPhysicalColumn(t *testing.T) {
	table := schema.MustTableDef("users", schema.Integer("id"))
	_, err := ResolveNames("generated", []schema.TableDef{table}, NameOverrides{Objects: map[schema.ObjectName]ObjectNameOverrides{{Name: "users"}: {Columns: map[string]ColumnNameOverrides{"missing": {Field: "Missing"}}}}})
	require.ErrorContains(t, err, "unknown column")
}
