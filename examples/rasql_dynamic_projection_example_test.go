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

// orderSummary reads one column from each side of the join, so no generated
// row type fits it: store.UsersRow has no user_id field and store.OrdersRow
// has no email. A local type names exactly the two columns the query selects.
type orderSummary struct {
	UserID int64
	Email  string
}

type orderSummaryDecoder struct{ result rasql.ResultSchema }

func (d orderSummaryDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d orderSummaryDecoder) Presence() []rasql.Presence       { return nil }
func (d orderSummaryDecoder) DecodeRow(src rasql.ScanSource, row *orderSummary) error {
	return src.Scan(&row.UserID, &row.Email)
}

func Example_rasql_dynamic_projection() {
	// This example joins users and orders, then reads an ad hoc result shape.
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
	users := store.Users()
	orders := store.Orders()
	// Create both descriptors before querying their joined rows.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if err := rasql.CreateTable(ctx, db, orders); err != nil {
		fmt.Printf("failed to create orders table: %s\n", err)
		return
	}

	// Populate both tables through the generated create builders.
	if _, err := store.Users().Create().ID(1).Email("ada@example.com").FirstName("First").LastName("Last").Exec(ctx, db); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}
	for _, order := range []store.OrdersRow{
		{ID: 1, UserID: 1, Total: 50},
		{ID: 2, UserID: 1, Total: 10},
	} {
		if _, err := store.Orders().Create().ID(order.ID).UserID(order.UserID).Total(order.Total).Exec(ctx, db); err != nil {
			fmt.Printf("failed to insert order: %s\n", err)
			return
		}
	}

	usersColumns, err := (store.UsersColumns{}).Bind(users)
	if err != nil {
		fmt.Printf("failed to bind users columns: %s\n", err)
		return
	}
	ordersColumns, err := (store.OrdersColumns{}).Bind(orders)
	if err != nil {
		fmt.Printf("failed to bind orders columns: %s\n", err)
		return
	}

	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "user_id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "email", Type: schema.TextType{}},
	)
	if err != nil {
		fmt.Printf("failed to build result schema: %s\n", err)
		return
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("user_id", usersColumns.ID.Expr(), schema.IntegerType{}, ""),
		rasql.Item("email", usersColumns.Email.Expr(), schema.TextType{}, ""),
	}, orderSummaryDecoder{result: result})
	if err != nil {
		fmt.Printf("failed to build projection: %s\n", err)
		return
	}

	// SQL: SELECT users.id AS user_id, users.email FROM users INNER JOIN orders ON users.id = orders.user_id WHERE orders.total > ? ORDER BY orders.total DESC (argument: 20)
	q := rasql.Select(users, projection).
		Join(orders, rasql.EqualExpr(usersColumns.ID.Expr(), ordersColumns.UserID.Expr())).
		Where(rasql.GreaterValue(ordersColumns.Total.Expr(), int64(20))).
		OrderBy(rasql.DescExpr(ordersColumns.Total.Expr()))
	rows, err := rasql.All(ctx, db, q)
	if err != nil {
		fmt.Printf("failed to build order totals query: %s\n", err)
		return
	}
	for _, summary := range rows {
		fmt.Println(summary.UserID, summary.Email)
	}

	// Output:
	// 1 ada@example.com
}
