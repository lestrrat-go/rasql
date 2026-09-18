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

// Example_rasql_query_errors shows where a failing query reports itself: the
// statement's own problems arrive as the error Rows returns, and everything
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

	db, err := rasql.Open(ctx, database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create executor: %s\n", err)
		return
	}
	users := store.Users()
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if _, err := store.Users().Create().ID(1).Email("ada@example.com").FirstName("First").LastName("Last").Exec(ctx, db); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	columns, err := (store.UsersColumns{}).Bind(users)
	if err != nil {
		fmt.Printf("failed to bind users columns: %s\n", err)
		return
	}
	projection, err := store.UsersProjection(columns)
	if err != nil {
		fmt.Printf("failed to build users projection: %s\n", err)
		return
	}
	base := rasql.Select(users, projection)

	rows, err := rasql.Rows(ctx, db, base)
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

	// Dropping the table shows which of the two checks catches an execution
	// failure. The statement still validates and renders, so Rows returns no
	// error and the database's complaint arrives on the first step of the loop.
	if _, err := database.ExecContext(ctx, "DROP TABLE users"); err != nil {
		fmt.Printf("failed to drop users table: %s\n", err)
		return
	}
	dropped, err := rasql.Rows(ctx, db, base)
	fmt.Println("error from Rows:", err)
	for _, err := range dropped {
		fmt.Println("error from the loop:", err)
	}

	// Output:
	// ada@example.com
	// error from Rows: <nil>
	// error from the loop: rasql: execute query: SQL logic error: no such table: users (1)
}
