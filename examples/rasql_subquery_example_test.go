package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_subquery() {
	// This example selects orders placed by a user reachable by email domain,
	// then narrows to orders at or above the average amount across every order.
	// users and UserRow are declared in query_example_tables_test.go with the
	// shape rasqlgen emits; an application that generated into package store
	// would write store.Users() and store.UsersRow instead.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A Client couples a database handle with the dialect used to render SQL.
	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}
	// A typed descriptor makes orders usable with rasql.Insert as well.
	type orderRow struct {
		ID     int `rasql:"id"`
		UserID int `rasql:"user_id"`
		Amount int `rasql:"amount"`
	}
	// A local result type projects only orders columns, so no join is needed:
	// both subqueries below run as their own SELECT, never as part of this one.
	type orderSummary struct {
		UserID int64
		Amount int64
	}
	orders := rasql.MustTable[orderRow](schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	})
	// Create both descriptors before querying orders against the users subquery.
	if err := rasql.Create(ctx, client, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if err := rasql.Create(ctx, client, orders); err != nil {
		fmt.Printf("failed to create orders table: %s\n", err)
		return
	}

	// orders has no generated column fields, so its columns are looked up by name.
	// That lookup validates them against the descriptor as the query is assembled.
	orderUserID, err := orders.Column("user_id")
	if err != nil {
		fmt.Printf("failed to find orders.user_id: %s\n", err)
		return
	}
	amount, err := orders.Column("amount")
	if err != nil {
		fmt.Printf("failed to find orders.amount: %s\n", err)
		return
	}

	for _, user := range []UserRow{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@other.example"},
	} {
		if _, err := rasql.Insert(ctx, client, users, user); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}
	for _, order := range []orderRow{
		{ID: 1, UserID: 1, Amount: 80},
		{ID: 2, UserID: 2, Amount: 20},
		{ID: 3, UserID: 3, Amount: 100},
	} {
		if _, err := rasql.Insert(ctx, client, orders, order); err != nil {
			fmt.Printf("failed to insert order: %s\n", err)
			return
		}
	}

	// domainUsers selects the id of every user whose email ends in the chosen
	// domain. It reads no table of the enclosing statement, so it validates and
	// renders as its own SELECT.
	domainUsers, err := query.NewSelect(users.QueryTable(), query.Project(users.ID))
	if err != nil {
		fmt.Printf("failed to build domain-users subquery: %s\n", err)
		return
	}
	domainUsers, err = domainUsers.WithWhere(query.Like(users.Email, query.Bind("%@example.com")))
	if err != nil {
		fmt.Printf("failed to filter domain-users subquery: %s\n", err)
		return
	}

	// allOrders aliases orders so the average subquery is a separate scope from
	// the orders read by the enclosing statement, even though it names the same
	// table.
	allOrders, err := rasql.As(orders, "all_orders")
	if err != nil {
		fmt.Printf("failed to alias orders: %s\n", err)
		return
	}
	allOrdersAmount, err := allOrders.Column("amount")
	if err != nil {
		fmt.Printf("failed to find all_orders.amount: %s\n", err)
		return
	}
	average, err := query.NewSelect(allOrders.QueryTable(), query.Project(query.Avg(allOrdersAmount)))
	if err != nil {
		fmt.Printf("failed to build average subquery: %s\n", err)
		return
	}

	// InSelect keeps orders placed by a domain user without costing one
	// argument per candidate id, and Scalar compares amount against the
	// average of every order.
	// SQL: SELECT orders.user_id, orders.amount FROM orders WHERE orders.user_id IN (SELECT users.id FROM users WHERE users.email LIKE ?) AND orders.amount >= (SELECT AVG(all_orders.amount) FROM orders AS all_orders) ORDER BY orders.amount ASC (argument: "%@example.com")
	rows, err := rasql.DecodeFrom[orderSummary](orders).
		Project(query.Project(orderUserID).As("user_id"), query.Project(amount)).
		Where(query.InSelect(orderUserID, domainUsers)).
		Where(query.GreaterThanOrEqual(amount, query.Scalar(average))).
		Order(query.Asc(amount)).
		Query(ctx, client)
	if err != nil {
		fmt.Printf("failed to query orders: %s\n", err)
		return
	}
	for summary, err := range rows {
		if err != nil {
			fmt.Printf("failed to query orders: %s\n", err)
			return
		}
		fmt.Println(summary.UserID, summary.Amount)
	}

	// Output:
	// 1 80
}
