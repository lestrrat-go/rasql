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

func Example_rasql_subquery() {
	// This example selects orders placed by a user reachable by email domain,
	// then narrows to orders at or above the average total across every order.
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
	// A local result type projects only orders columns, so no join is needed:
	// both subqueries below run as their own SELECT, never as part of this one.
	// store.OrdersRow would decode these two columns as well, but it maps the
	// whole table, so its id field would read 0 whether or not the database
	// sent one. A type holding just the projected columns says what was asked
	// for.
	type orderSummary struct {
		UserID int64
		Total  int64
	}
	// Create both descriptors before querying orders against the users subquery.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if err := rasql.CreateTable(ctx, db, orders); err != nil {
		fmt.Printf("failed to create orders table: %s\n", err)
		return
	}

	for _, user := range []store.UsersRow{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@other.example"},
	} {
		if _, err := rasql.Insert(ctx, db, users, user); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}
	for _, order := range []store.OrdersRow{
		{ID: 1, UserID: 1, Total: 80},
		{ID: 2, UserID: 2, Total: 20},
		{ID: 3, UserID: 3, Total: 100},
	} {
		if _, err := rasql.Insert(ctx, db, orders, order); err != nil {
			fmt.Printf("failed to insert order: %s\n", err)
			return
		}
	}

	// domainUsers selects the id of every user whose email ends in the chosen
	// domain. It reads no table of the enclosing statement, so it validates and
	// renders as its own SELECT.
	domainUsers, err := query.NewSelect(users.Ref(), users.ID())
	if err != nil {
		fmt.Printf("failed to build domain-users subquery: %s\n", err)
		return
	}
	domainUsers, err = domainUsers.WithWhere(query.Like(users.Email(), "%@example.com"))
	if err != nil {
		fmt.Printf("failed to filter domain-users subquery: %s\n", err)
		return
	}

	// allOrders aliases orders so the average subquery is a separate scope from
	// the orders read by the enclosing statement, even though it names the same
	// table.
	allOrders, err := orders.As("all_orders")
	if err != nil {
		fmt.Printf("failed to alias orders: %s\n", err)
		return
	}
	average, err := query.NewSelect(allOrders.Ref(), query.Avg(allOrders.Total()))
	if err != nil {
		fmt.Printf("failed to build average subquery: %s\n", err)
		return
	}

	// InSelect keeps orders placed by a domain user without costing one
	// argument per candidate id, and Scalar compares the total against the
	// average of every order.
	// SQL: SELECT orders.user_id, orders.total FROM orders WHERE orders.user_id IN (SELECT users.id FROM users WHERE users.email LIKE ?) AND orders.total >= (SELECT AVG(all_orders.total) FROM orders AS all_orders) ORDER BY orders.total ASC (argument: "%@example.com")
	rows, err := rasql.DecodeFrom[orderSummary](orders).
		Project(orders.UserID().As("user_id"), orders.Total()).
		Where(query.InSelect(orders.UserID(), domainUsers)).
		Where(query.GreaterThanOrEqual(orders.Total(), query.Scalar(average))).
		Order(query.Asc(orders.Total())).
		Query(ctx, db)
	if err != nil {
		fmt.Printf("failed to query orders: %s\n", err)
		return
	}
	for summary, err := range rows {
		if err != nil {
			fmt.Printf("failed to query orders: %s\n", err)
			return
		}
		fmt.Println(summary.UserID, summary.Total)
	}

	// Output:
	// 1 80
}
