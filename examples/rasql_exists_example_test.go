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

func Example_rasql_exists() {
	// This example lists the users who have placed at least one order, then
	// the users who have placed none, from one correlated subquery used twice.
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
	// A local result type projects the two user columns this example prints.
	type userSummary struct {
		ID    int64
		Email string
	}
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
		{ID: 3, Email: "cyd@example.com"},
	} {
		if _, err := rasql.Insert(ctx, db, users, user); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}
	for _, order := range []store.OrdersRow{
		{ID: 1, UserID: 1, Total: 80},
		{ID: 2, UserID: 3, Total: 100},
	} {
		if _, err := rasql.Insert(ctx, db, orders, order); err != nil {
			fmt.Printf("failed to insert order: %s\n", err)
			return
		}
	}

	// hasOrder reads the orders of the user the enclosing statement is on.
	// WithCorrelation names that enclosing table first, because WithWhere
	// validates the predicate it is given and no statement encloses this one
	// yet. EXISTS reads no value, so the projection is arbitrary; a column of
	// the subquery's own table costs no parameter and renders the same on every
	// engine.
	hasOrder, err := query.NewSelect(orders.Ref(), orders.ID())
	if err != nil {
		fmt.Printf("failed to build the orders subquery: %s\n", err)
		return
	}
	hasOrder, err = hasOrder.WithCorrelation(users.Ref())
	if err != nil {
		fmt.Printf("failed to correlate the orders subquery: %s\n", err)
		return
	}
	hasOrder, err = hasOrder.WithWhere(query.Equal(orders.UserID(), users.ID()))
	if err != nil {
		fmt.Printf("failed to filter the orders subquery: %s\n", err)
		return
	}

	// SQL: SELECT users.id, users.email FROM users WHERE EXISTS (SELECT orders.id FROM orders WHERE orders.user_id = users.id) ORDER BY users.id ASC
	buyers, err := rasql.DecodeFrom[userSummary](users).
		Project(users.ID(), users.Email()).
		Where(query.Exists(hasOrder)).
		Order(query.Asc(users.ID())).
		All(ctx, db)
	if err != nil {
		fmt.Printf("failed to query users with an order: %s\n", err)
		return
	}
	for _, buyer := range buyers {
		fmt.Println("ordered:", buyer.ID, buyer.Email)
	}

	// The same subquery under NOT EXISTS answers the opposite question, and it
	// is still evaluated once per user rather than once for the statement.
	quiet, err := rasql.DecodeFrom[userSummary](users).
		Project(users.ID(), users.Email()).
		Where(query.NotExists(hasOrder)).
		Order(query.Asc(users.ID())).
		All(ctx, db)
	if err != nil {
		fmt.Printf("failed to query users without an order: %s\n", err)
		return
	}
	for _, user := range quiet {
		fmt.Println("no order:", user.ID, user.Email)
	}

	// Output:
	// ordered: 1 ada@example.com
	// ordered: 3 cyd@example.com
	// no order: 2 bob@example.com
}
