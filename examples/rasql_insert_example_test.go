package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
)

func Example_rasql_insert() {
	// This example inserts one generated row without constructing query.Insert.
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
	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}
	// Create the table described by the generated users reference.
	if err := rasql.Create(ctx, client, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	// Insert uses the tagged fields in UserRow as values for the users table.
	result, err := rasql.Insert(ctx, client, users, UserRow{ID: 42, Email: "ada@example.com"})
	if err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		fmt.Printf("failed to count inserted users: %s\n", err)
		return
	}
	fmt.Printf("%d user inserted\n", inserted)

	// Output:
	// 1 user inserted
}
