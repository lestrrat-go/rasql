package examples_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_no_rows() {
	// This example queries an empty users table and shows how to branch on a
	// missing row. users and UserRow are declared in
	// query_example_tables_test.go with the shape rasqlgen emits; an
	// application that generated into package store would write
	// store.Users() and store.UsersRow instead.
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
	// Create the users table, but never insert into it, so One matches no row.
	if err := rasql.Create(ctx, client, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	_, err = rasql.SelectFrom(users).WhereEqual(users.ID, 1).One(ctx, client)
	if errors.Is(err, rasql.ErrNoRows) {
		fmt.Println("no such user")
	}
	// rasql.ErrNoRows wraps database/sql.ErrNoRows, so a caller that already
	// branches on the standard library's sentinel keeps working unchanged.
	fmt.Println("also sql.ErrNoRows:", errors.Is(err, sql.ErrNoRows))

	// Output:
	// no such user
	// also sql.ErrNoRows: true
}
