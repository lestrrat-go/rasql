//go:build unix

package inspect_test

import (
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestMySQLInspectorRecordsIntegerDisplayWidthAndZeroFillAgainstLiveDatabase
// pins what a real MySQL server reports back for an INT ZEROFILL column,
// because TestMySQLInspectorRecordsIntegerDisplayWidthAndZeroFill in
// inspect_test.go only asserts rasql's own mocked catalog echoes what the
// test told it to. Before this feature existed, inspecting this column
// failed outright: ZEROFILL made the whole table unrepresentable, which is
// the exact failure a sweep over a production schema must not hit on its
// first display-width or ZEROFILL column.
//
// MySQL 8.0.19+ deprecates the integer display width and may no longer
// report one back for a plain integer column, though it is documented to
// still matter, and still be reported, for a column carrying ZEROFILL. This
// reads the live catalog's own COLUMN_TYPE first, rather than assume a
// width rasql cannot yet confirm this server's version kept, so the
// assertion below matches whatever MySQL 8.4 (see compose.yaml) actually
// does instead of a guess that could stop passing on a version bump.
func TestMySQLInspectorRecordsIntegerDisplayWidthAndZeroFillAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.MySQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE zerofill_counters (id INT PRIMARY KEY, total INT(11) ZEROFILL NOT NULL) ENGINE=InnoDB")

	var columnType string
	err := database.QueryRowContext(ctx,
		"SELECT column_type FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?",
		"zerofill_counters", "total",
	).Scan(&columnType)
	require.NoError(t, err, "read live COLUMN_TYPE")

	wantWidth := schema.IntegerDisplayWidth{}
	if strings.Contains(columnType, "(11)") {
		wantWidth = schema.NewIntegerDisplayWidth(11)
	}

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "zerofill_counters")
	require.NoError(t, err)

	var total schema.ColumnDef
	for _, candidate := range table.Columns {
		if candidate.Name == "total" {
			total = candidate
		}
	}
	require.Equal(t, "total", total.Name, "inspected table has a total column")
	require.Equal(t, schema.IntegerType{Unsigned: true, DisplayWidth: wantWidth, ZeroFill: true}, total.Type,
		"the live catalog reports ZEROFILL as implicitly unsigned, with whatever display width this MySQL version still states")
}
