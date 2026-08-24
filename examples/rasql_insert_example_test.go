package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_insert() {
	// This example inserts one generated row without constructing query.Insert.
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
	// Create the table described by the generated users descriptor.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	// Insert reads store.UsersRow's fields through the mapping method the
	// generator wrote, and binds them as values for the users table.
	// SQL: INSERT INTO users (id, email) VALUES (?, ?) (arguments: 42, "ada@example.com")
	result, err := rasql.Insert(ctx, db, users, store.UsersRow{ID: 42, Email: "ada@example.com"})
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
