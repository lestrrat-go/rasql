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

// TestMySQLInspectorRecordsDecimalUnsignedAndZeroFillAgainstLiveDatabase pins
// what a real MySQL server reports back for a DECIMAL column's UNSIGNED and
// ZEROFILL attributes, because the sqlmock-based
// TestMySQLInspectorRecordsDecimalUnsignedAndZeroFill in inspect_test.go only
// asserts rasql's own mocked catalog echoes what the test told it to. Before
// this feature existed, inspecting either of these columns failed outright:
// an UNSIGNED or ZEROFILL decimal column made the whole table
// unrepresentable, which is the exact failure a sweep over a production
// schema must not hit on its first decimal column carrying one.
func TestMySQLInspectorRecordsDecimalUnsignedAndZeroFillAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.MySQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE invoices ("+
		"id INTEGER PRIMARY KEY, "+
		"unsigned_amount DECIMAL(10,2) UNSIGNED NOT NULL, "+
		"zerofill_amount DECIMAL(10,2) UNSIGNED ZEROFILL NOT NULL"+
		")")

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "invoices")
	require.NoError(t, err)
	require.Equal(t, []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "unsigned_amount", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2), Unsigned: true}},
		{Name: "zerofill_amount", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2), Unsigned: true, ZeroFill: true}},
	}, table.Columns)
}
