package rasql_test

import (
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type viewCapabilityRow struct {
	ID int64 `rasql:"id"`
}

func TestTableConstructorsRequireWriteCapabilities(t *testing.T) {
	view := schema.TableDef{
		Name:       "active_users",
		Kind:       schema.ObjectView,
		Operations: schema.OperationRead,
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	}
	read, err := rasql.ReadTableOf[viewCapabilityRow](view)
	require.NoError(t, err)
	require.NotNil(t, read)
	_, err = rasql.TableOf[viewCapabilityRow](view)
	require.ErrorContains(t, err, "does not support operation")
	require.Panics(t, func() { rasql.MustTableOf[viewCapabilityRow](view) })

	writableView := view
	writableView.Operations = schema.OperationRead | schema.OperationInsert | schema.OperationUpdate | schema.OperationDelete
	table, err := rasql.TableOf[viewCapabilityRow](writableView)
	require.NoError(t, err)
	require.NotNil(t, table)
}
