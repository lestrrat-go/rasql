package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// Example_rasql_query_errors shows where a failing query reports itself: the
// statement's own problems arrive as the error Query returns, and everything
// that goes wrong once rows are moving arrives inside the loop.
func Example_rasql_query_errors() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if _, err := rasql.Insert(ctx, db, users, UserRow{ID: 1, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// BEGIN(query_errors)
	rows, err := rasql.SelectFrom(users).Query(ctx, db)
	if err != nil {
		// The statement could not be validated or rendered.
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	for user, err := range rows {
		if err != nil {
			// Execution or scanning failed. No further rows follow.
			fmt.Printf("failed to read user: %s\n", err)
			return
		}
		fmt.Println(user.Email)
	}
	// END(query_errors)

	// Dropping the table shows which of the two checks catches an execution
	// failure. The statement still validates and renders, so Query returns no
	// error and the database's complaint arrives on the first step of the loop.
	if _, err := database.ExecContext(ctx, "DROP TABLE users"); err != nil {
		fmt.Printf("failed to drop users table: %s\n", err)
		return
	}
	dropped, err := rasql.SelectFrom(users).Query(ctx, db)
	fmt.Println("error from Query:", err)
	for _, err := range dropped {
		fmt.Println("error from the loop:", err)
	}

	// Output:
	// ada@example.com
	// error from Query: <nil>
	// error from the loop: rasql: execute query: SQL logic error: no such table: users (1)
}
