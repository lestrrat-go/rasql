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
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	client, err := runtime.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create runtime client: %s\n", err)
		return
	}
	orders := query.MustNewTableRef(schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
			{Name: "total", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	if err := client.CreateTable(ctx, users.Ref().Table()); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if err := client.CreateTable(ctx, orders.Table()); err != nil {
		fmt.Printf("failed to create orders table: %s\n", err)
		return
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO users (id, email) VALUES (?, ?)", 1, "ada@example.com"); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO orders (id, user_id, total) VALUES (?, ?, ?), (?, ?, ?)", 1, 1, 50, 2, 1, 10); err != nil {
		fmt.Printf("failed to insert orders: %s\n", err)
		return
	}

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
	orderUserID, err := orders.Column("user_id")
	if err != nil {
		fmt.Printf("failed to find orders.user_id: %s\n", err)
		return
	}
	total, err := orders.Column("total")
	if err != nil {
		fmt.Printf("failed to find orders.total: %s\n", err)
		return
	}

	// Use the raw builder when a join or projection has no single row type.
	rows, err := client.SelectFrom(users.Ref()).
		Join(query.InnerJoin(orders, query.Equal(userID, orderUserID))).
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
