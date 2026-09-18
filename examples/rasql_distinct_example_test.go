package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// orderingUser holds one row per distinct user id. store.OrdersRow would
// decode this projection too, but it maps the whole table, so id and total
// would come back as zeroes no caller could tell from stored zeroes. A type
// with only the projected field cannot misreport what was selected.
type orderingUser struct {
	UserID int64
}

type orderingUserDecoder struct{ result rasql.ResultSchema }

func (d orderingUserDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d orderingUserDecoder) Presence() []rasql.Presence       { return nil }
func (d orderingUserDecoder) DecodeRow(src rasql.ScanSource, row *orderingUser) error {
	return src.Scan(&row.UserID)
}

func Example_rasql_distinct() {
	// This example lists the users who have placed at least one order,
	// without repeating a user who placed more than one.
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
	orders := store.Orders()
	if err := rasql.CreateTable(ctx, db, orders); err != nil {
		fmt.Printf("failed to create orders table: %s\n", err)
		return
	}
	for _, order := range []store.OrdersRow{
		{ID: 1, UserID: 1},
		{ID: 2, UserID: 2},
		{ID: 3, UserID: 1},
	} {
		plan, err := store.Orders().Create().ID(order.ID).UserID(order.UserID).Total(order.Total).Plan()
		if err != nil {
			fmt.Printf("failed to build insert: %s\n", err)
			return
		}
		if _, err := rasql.Exec(ctx, db, plan); err != nil {
			fmt.Printf("failed to insert order: %s\n", err)
			return
		}
	}

	ordersColumns, err := (store.OrdersColumns{}).Bind(orders)
	if err != nil {
		fmt.Printf("failed to bind orders columns: %s\n", err)
		return
	}
	result, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "user_id", Type: schema.IntegerType{}})
	if err != nil {
		fmt.Printf("failed to build result schema: %s\n", err)
		return
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("user_id", ordersColumns.UserID.Expr(), schema.IntegerType{}, ""),
	}, orderingUserDecoder{result: result})
	if err != nil {
		fmt.Printf("failed to build projection: %s\n", err)
		return
	}

	// Distinct is meaningful here because the projection narrows the result to
	// user_id alone; a full-row select would already select the orders
	// primary key, which makes every row unique before DISTINCT runs.
	// SQL: SELECT DISTINCT orders.user_id FROM orders ORDER BY orders.user_id
	q := rasql.Select(orders, projection).Distinct().OrderBy(rasql.AscExpr(ordersColumns.UserID.Expr()))
	rows, err := rasql.All(ctx, db, q)
	if err != nil {
		fmt.Printf("failed to query ordering users: %s\n", err)
		return
	}
	for _, found := range rows {
		fmt.Println(found.UserID)
	}

	// Output:
	// 1
	// 2
}
