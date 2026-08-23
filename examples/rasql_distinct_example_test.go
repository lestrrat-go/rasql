package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_distinct() {
	// This example lists the users who have placed at least one order,
	// without repeating a user who placed more than one. orders and OrderRow
	// are declared in query_example_tables_test.go with the shape rasqlgen
	// emits; an application that generated into package store would write
	// store.Orders() and store.OrdersRow instead.
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

	// A local result type holds one row per distinct user id.
	type orderingUser struct {
		UserID int64 `rasql:"user_id"`
	}
	if err := rasql.CreateTable(ctx, db, orders); err != nil {
		fmt.Printf("failed to create orders table: %s\n", err)
		return
	}
	for _, order := range []OrderRow{
		{ID: 1, UserID: 1},
		{ID: 2, UserID: 2},
		{ID: 3, UserID: 1},
	} {
		if _, err := rasql.Insert(ctx, db, orders, order); err != nil {
			fmt.Printf("failed to insert order: %s\n", err)
			return
		}
	}

	// Distinct is meaningful here because Project narrows the result to
	// user_id alone; SelectFrom would already select the orders primary key,
	// which makes every row unique before DISTINCT runs.
	// SQL: SELECT DISTINCT orders.user_id FROM orders ORDER BY orders.user_id
	rows, err := rasql.DecodeFrom[orderingUser](orders).
		Project(orders.UserID().As("user_id")).
		Distinct().
		Order(query.Asc(orders.UserID())).
		Query(ctx, db)
	if err != nil {
		fmt.Printf("failed to query ordering users: %s\n", err)
		return
	}
	for found, err := range rows {
		if err != nil {
			fmt.Printf("failed to query ordering users: %s\n", err)
			return
		}
		fmt.Println(found.UserID)
	}

	// Output:
	// 1
	// 2
}
