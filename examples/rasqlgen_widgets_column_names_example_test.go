package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// Example_rasqlgen_widgets_column_names reads and writes a table whose column
// names collide with names the generated package already uses.
//
// The "widgets" table has three such columns. "ref" matches the generated
// table's own Ref method, "scan_row" matches the generated row type's ScanRow
// method, and "plan" matches the create and patch builders' Plan method. Each
// costs something different, and none of them stops the column being read or
// written.
//
// `rasql codegen generate` prints a line for every column that cost anything,
// so the two below are reported and "ref", which costs nothing, is not.
func Example_rasqlgen_widgets_column_names() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	db, err := rasql.Open(ctx, database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create executor: %s\n", err)
		return
	}
	widgets := store.Widgets()
	if err := rasql.CreateTable(ctx, db, widgets); err != nil {
		fmt.Printf("failed to create widgets table: %s\n", err)
		return
	}

	// BEGIN(widgets_write)

	// "plan" has no setter on the create builder, because the builder's own
	// Plan method already has that name. rasql.SetField writes it instead,
	// against the same column field the setter would have used, and
	// WidgetsHandle hands the typed table to the constructor.
	//
	// widgets.Ref is the table's own Ref method, so the column behind it is
	// named through the embedded WidgetsExpressions struct. ScanRow and Plan
	// are not methods of the table, so those need no such qualifying.
	create, err := rasql.NewCreatePlan(store.WidgetsHandle(widgets),
		rasql.SetField(widgets.WidgetsExpressions.Ref, "first"),
		rasql.SetField(widgets.ScanRow, "scanned"),
		rasql.SetField(widgets.Plan, "annual"),
	)
	if err != nil {
		fmt.Printf("failed to build the insert: %s\n", err)
		return
	}
	if _, err := rasql.Exec(ctx, db, create); err != nil {
		fmt.Printf("failed to insert the row: %s\n", err)
		return
	}
	// END(widgets_write)

	// BEGIN(widgets_read)

	// Reading is unaffected. Every column is a field of the row type, so the
	// generated projection selects all three and rasql.All returns them.
	//
	// A predicate needs the column rather than the value, and naming the
	// embedded WidgetsExpressions struct is what reaches it: widgets.Ref on
	// its own would be the table's own Ref method.
	projection, err := store.WidgetsProjection(widgets)
	if err != nil {
		fmt.Printf("failed to build the projection: %s\n", err)
		return
	}
	// SQL: SELECT widgets.id, widgets.ref, widgets.scan_row, widgets.plan FROM widgets WHERE widgets.ref = ? (argument: "first")
	rows, err := rasql.All(ctx, db, rasql.Select(widgets, projection).
		Where(rasql.EqualValue(widgets.WidgetsExpressions.Ref.Expr(), "first")))
	if err != nil {
		fmt.Printf("failed to query the widgets table: %s\n", err)
		return
	}
	for _, row := range rows {
		fmt.Printf("ref=%s scan_row=%s plan=%s\n", row.Ref, row.ScanRow, row.Plan)
	}
	// END(widgets_read)

	// The table itself is untouched by any of this, so a column is also
	// reachable by its database name, which is what the query package and the
	// dynamic builders take.
	fmt.Println(widgets.Ref().Column("plan").Validate())

	// Output:
	// ref=first scan_row=scanned plan=annual
	// <nil>
}
