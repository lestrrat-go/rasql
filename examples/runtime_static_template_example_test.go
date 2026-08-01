package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/row"
	"github.com/lestrrat-go/rasql/runtime"
	querytemplate "github.com/lestrrat-go/rasql/template"
)

func Example_runtime_static_template() {
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
	if _, err := runtime.Insert(ctx, client, users, UserRow{ID: 42, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	parsed, err := querytemplate.Parse("user_by_email", "SELECT id, email FROM users WHERE email = {{bind \"email\"}}")
	if err != nil {
		fmt.Printf("failed to parse template: %s\n", err)
		return
	}
	compiled, err := parsed.Compile(dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to compile template: %s\n", err)
		return
	}
	statement, err := compiled.Bind(map[string]any{"email": "ada@example.com"})
	if err != nil {
		fmt.Printf("failed to bind template: %s\n", err)
		return
	}

	// QueryRendered executes the dialect-specific statement produced by the template.
	rows, err := client.QueryRendered(ctx, statement)
	if err != nil {
		fmt.Printf("failed to query user: %s\n", err)
		return
	}
	email, err := row.Get[string](rows[0], "email")
	if err != nil {
		fmt.Printf("failed to read email: %s\n", err)
		return
	}
	fmt.Println(email)

	// Output:
	// ada@example.com
}
