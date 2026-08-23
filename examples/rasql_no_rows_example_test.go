package examples_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_no_rows() {
	// This example queries an empty users table and shows how to branch on a
	// missing row.
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
	// Create the users table, but never insert into it, so One matches no row.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	// SQL: SELECT users.id, users.email FROM users WHERE users.id = ? (argument: 1)
	// BEGIN(no_rows)
	_, err = rasql.SelectFrom(users).WhereEqual(users.ID(), 1).One(ctx, db)
	if errors.Is(err, rasql.ErrNoRows) {
		fmt.Println("no such user")
	}
	// END(no_rows)
	// rasql.ErrNoRows wraps database/sql.ErrNoRows, so a caller that already
	// branches on the standard library's sentinel keeps working unchanged.
	fmt.Println("also sql.ErrNoRows:", errors.Is(err, sql.ErrNoRows))

	// Output:
	// no such user
	// also sql.ErrNoRows: true
}
