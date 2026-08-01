package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/runtime"
)

func Example_runtime_typed_query() {
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
	if err := client.CreateTable(ctx, users.Ref().Table()); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO users (id, email) VALUES (?, ?), (?, ?), (?, ?)", 1, "ada@example.com", 2, "bob@example.com", 3, "cyd@example.com"); err != nil {
		fmt.Printf("failed to insert users: %s\n", err)
		return
	}

	// SelectFrom knows the UsersRow result type from users. It selects every
	// column, then All decodes every matching row into that type.
	found, err := runtime.SelectFrom(client, users).
		OrderAsc("email").
		Offset(1).
		Limit(2).
		All(ctx)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	for _, user := range found {
		fmt.Println(user.Email)
	}

	// Output:
	// bob@example.com
	// cyd@example.com
}
