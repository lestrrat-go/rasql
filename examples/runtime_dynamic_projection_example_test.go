package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/row"
	"github.com/lestrrat-go/rasql/runtime"
	"github.com/lestrrat-go/rasql/schema"
)

func Example_runtime_dynamic_projection() {
	// This example joins users and orders, then reads an ad hoc result shape.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer database.Close()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A Client couples a database handle with the dialect used to render SQL.
	client, err := runtime.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create runtime client: %s\n", err)
		return
	}
	// A typed descriptor makes orders usable with runtime.Insert as well.
	type orderRow struct {
		ID     int `rasql:"id"`
		UserID int `rasql:"user_id"`
		Total  int `rasql:"total"`
	}
	orders := runtime.MustTable[orderRow](query.MustNewTableRef(schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
			{Name: "total", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	}))
	// Create both descriptors before querying their joined rows.
	if err := runtime.Create(ctx, client, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if err := runtime.Create(ctx, client, orders); err != nil {
		fmt.Printf("failed to create orders table: %s\n", err)
		return
	}

	// Column references keep the dynamic query validated as it is assembled.
	userID, err := users.Ref().Column("id")
	if err != nil {
		fmt.Printf("failed to find users.id: %s\n", err)
		return
	}
	email, err := users.Ref().Column("email")
	if err != nil {
		fmt.Printf("failed to find users.email: %s\n", err)
		return
	}
	orderUserID, err := orders.Ref().Column("user_id")
	if err != nil {
		fmt.Printf("failed to find orders.user_id: %s\n", err)
		return
	}
	total, err := orders.Ref().Column("total")
	if err != nil {
		fmt.Printf("failed to find orders.total: %s\n", err)
		return
	}
	// Populate both tables through the typed runtime API.
	if _, err := runtime.Insert(ctx, client, users, UserRow{ID: 1, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}
	for _, order := range []orderRow{
		{ID: 1, UserID: 1, Total: 50},
		{ID: 2, UserID: 1, Total: 10},
	} {
		if _, err := runtime.Insert(ctx, client, orders, order); err != nil {
			fmt.Printf("failed to insert order: %s\n", err)
			return
		}
	}

	// Use the raw builder when a join or projection has no single row type.
	rows, err := client.SelectFrom(users.Ref()).
		Join(query.InnerJoin(orders.Ref(), query.Equal(userID, orderUserID))).
		Project(query.Project(userID), query.Project(email)).
		Where(query.GreaterThan(total, query.Bind(20))).
		Order(query.Desc(total)).
		Query(ctx)
	if err != nil {
		fmt.Printf("failed to query order totals: %s\n", err)
		return
	}
	userIDValue, err := row.Get[int64](rows[0], "id")
	if err != nil {
		fmt.Printf("failed to read user ID: %s\n", err)
		return
	}
	emailValue, err := row.Get[string](rows[0], "email")
	if err != nil {
		fmt.Printf("failed to read email: %s\n", err)
		return
	}
	fmt.Println(userIDValue, emailValue)

	// Output:
	// 1 ada@example.com
}
