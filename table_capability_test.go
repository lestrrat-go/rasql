package rasql_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
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

func TestForgedWritableHandleRejectsEveryMutation(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, database.Close()) })
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	view := schema.TableDef{Name: "active_users", Kind: schema.ObjectView, Operations: schema.OperationRead, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}}
	for name, call := range map[string]func() error{
		"insert": func() error {
			_, err := rasql.Insert(t.Context(), db, rasql.TableFrom[viewCapabilityRow](view), viewCapabilityRow{ID: 1})
			return err
		},
		"update": func() error {
			_, err := rasql.Update(t.Context(), db, rasql.TableFrom[viewCapabilityRow](view), viewCapabilityRow{ID: 1})
			return err
		},
		"delete": func() error {
			_, err := rasql.DeleteFrom(rasql.TableFrom[viewCapabilityRow](view)).AllowAll().Exec(t.Context(), db)
			return err
		},
		"ddl": func() error { return rasql.CreateTable(t.Context(), db, rasql.TableFrom[viewCapabilityRow](view)) },
	} {
		t.Run(name, func(t *testing.T) { require.ErrorContains(t, call(), "does not support") })
	}
}
