//go:build unix

package inspect_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestPostgreSQLInspectorRecordsForeignKeyDeleteSetColumnsAgainstLiveDatabase
// pins what a real PostgreSQL 16+ server reports back for a foreign key's
// ON DELETE SET NULL (columns) clause, because the sqlmock-based
// TestPostgreSQLInspectorRecordsForeignKeyTemporalAndDeleteSetColumns in
// inspect_test.go only asserts rasql's own mocked catalog echoes what the
// test told it to. Before this feature existed, inspecting this table
// failed outright: the column list on ON DELETE SET NULL made the whole
// table unrepresentable, which is the exact failure a sweep over a
// production schema must not hit on its first such foreign key.
func TestPostgreSQLInspectorRecordsForeignKeyDeleteSetColumnsAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE customers (id integer PRIMARY KEY)")
	mustExec(t, ctx, database, "CREATE TABLE orders (id integer PRIMARY KEY, customer_id integer)")
	mustExec(t, ctx, database, "ALTER TABLE orders ADD CONSTRAINT orders_customer_fk FOREIGN KEY (customer_id) REFERENCES customers (id) ON DELETE SET NULL (customer_id)")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "orders")
	require.NoError(t, err)
	require.Equal(t, []schema.ForeignKeyDef{
		{
			Name:              "orders_customer_fk",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
			OnDelete:          schema.SetNull,
			DeleteSetColumns:  []string{"customer_id"},
		},
	}, table.ForeignKeys)
}
