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

	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 40, 0)
	if err != nil {
		fmt.Printf("failed to describe engine profile: %s\n", err)
		return
	}
	executor, err := rasql.AsExecutor(db, profile)
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
		plan := store.NewOrdersCreate().ID(order.ID).UserID(order.UserID).Total(order.Total).Plan()
		if _, err := rasql.ExecMutation(ctx, executor, plan); err != nil {
			fmt.Printf("failed to insert order: %s\n", err)
			return
		}
	}

	source, err := rasql.SourceOf(orders, "")
	if err != nil {
		fmt.Printf("failed to bind orders source: %s\n", err)
		return
	}
	userID, err := rasql.BindColumn[store.OrdersRow, int64](source, orders.UserIDRef().Name(), "")
	if err != nil {
		fmt.Printf("failed to bind user_id column: %s\n", err)
		return
	}
	result, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "user_id", Type: schema.IntegerType{}})
	if err != nil {
		fmt.Printf("failed to build result schema: %s\n", err)
		return
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("user_id", userID.Expr(), schema.IntegerType{}, ""),
	}, orderingUserDecoder{result: result})
	if err != nil {
		fmt.Printf("failed to build projection: %s\n", err)
		return
	}

	// Distinct is meaningful here because the projection narrows the result to
	// user_id alone; a full-row select would already select the orders
	// primary key, which makes every row unique before DISTINCT runs.
	// SQL: SELECT DISTINCT orders.user_id FROM orders ORDER BY orders.user_id
	q := rasql.Select(source.Source(), projection).Distinct().OrderBy(rasql.AscExpr(userID.Expr()))
	rows, err := rasql.All(ctx, executor, q)
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
