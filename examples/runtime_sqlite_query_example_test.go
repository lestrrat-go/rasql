package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/row"
	"github.com/lestrrat-go/rasql/runtime"
)

func Example_runtime_sqlite_query() {
	ctx := context.Background()
	// Importing runtime registers the pure-Go SQLite driver as "sqlite".
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
	if err := client.CreateTable(ctx, users.Table()); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO users (id, email) VALUES (?, ?)", 42, "ada@example.com"); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	rows, err := client.SelectFrom(users).
		Select("id", "email").
		WhereEqual("id", 42).
		Query(ctx)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	if len(rows) != 1 {
		fmt.Printf("expected one user, got %d\n", len(rows))
		return
	}
	email, err := row.String("email")
	if err != nil {
		fmt.Printf("failed to create email column: %s\n", err)
		return
	}
	userEmail, err := email.Get(rows[0])
	if err != nil {
		fmt.Printf("failed to read email: %s\n", err)
		return
	}

	fmt.Println(userEmail)

	// Output:
	// ada@example.com
}
