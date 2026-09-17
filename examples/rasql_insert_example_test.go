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
	db, err := rasql.Open(ctx, database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create executor: %s\n", err)
		return
	}
	users := store.Users()
	// Create the table described by the generated users descriptor.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	// The generated create builder binds the row's fields as values, through
	// the columns the generator bound for it.
	// SQL: INSERT INTO users (id, email) VALUES (?, ?) (arguments: 42, "ada@example.com")
	plan, err := store.NewUsersCreate().ID(42).Email("ada@example.com").FirstName("First").LastName("Last").Plan()
	if err != nil {
		fmt.Printf("failed to build insert: %s\n", err)
		return
	}
	outcome, err := rasql.ExecMutation(ctx, db, plan)
	if err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}
	fmt.Printf("%d user inserted\n", outcome.Affected)

	// Output:
	// 1 user inserted
}
