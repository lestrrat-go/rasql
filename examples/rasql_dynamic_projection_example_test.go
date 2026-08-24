package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/query"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

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

	// A DB couples a database handle with the dialect used to render SQL.
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	users := store.Users()
	orders := store.Orders()
	// A local result type makes the custom projection as easy to read as a table row.
	type orderSummary struct {
		UserID int64
		Email  string
	}
	// Create both descriptors before querying their joined rows.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if err := rasql.CreateTable(ctx, db, orders); err != nil {
		fmt.Printf("failed to create orders table: %s\n", err)
		return
	}

	// Populate both tables through the typed rasql API.
	if _, err := rasql.Insert(ctx, db, users, store.UsersRow{ID: 1, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}
	for _, order := range []store.OrdersRow{
		{ID: 1, UserID: 1, Total: 50},
		{ID: 2, UserID: 1, Total: 10},
	} {
		if _, err := rasql.Insert(ctx, db, orders, order); err != nil {
			fmt.Printf("failed to insert order: %s\n", err)
			return
		}
	}

	// DecodeFrom maps the selected names into orderSummary's exported fields.
	// SQL: SELECT users.id AS user_id, users.email FROM users INNER JOIN orders ON users.id = orders.user_id WHERE orders.total > ? ORDER BY orders.total DESC (argument: 20)
	rows, err := rasql.DecodeFrom[orderSummary](users).
		Join(rasql.InnerJoin(orders, query.Equal(users.ID(), orders.UserID()))).
		Project(users.ID().As("user_id"), users.Email()).
		Where(query.GreaterThan(orders.Total(), 20)).
		Order(query.Desc(orders.Total())).
		Query(ctx, db)
	if err != nil {
		fmt.Printf("failed to build order totals query: %s\n", err)
		return
	}
	for summary, err := range rows {
		if err != nil {
			fmt.Printf("failed to query order totals: %s\n", err)
			return
		}
		fmt.Println(summary.UserID, summary.Email)
	}

	// Output:
	// 1 ada@example.com
}
