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

// Example_rasql_insert_defaults writes a row whose id the database assigns and
// whose status comes from the column's default. The users table generated into
// examples/store declares status with that default.
func Example_rasql_insert_defaults() {
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
	users := store.Users()
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	// Name each column the database is to fill in. Every other column is
	// written from the row, so the empty strings below are inserted as given.
	// SQL: INSERT INTO users (email, nickname, first_name, last_name) VALUES (?, ?, ?, ?) (arguments: "", NULL, "", "")
	if _, err := rasql.InsertWithOptions(ctx, db, users, store.UsersRow{}, rasql.DefaultColumns("id", "status")); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// SQL: SELECT users.id, users.email, users.nickname, users.status, users.first_name, users.last_name FROM users WHERE users.id = ? (argument: 1)
	user, err := rasql.SelectFrom(users).WhereEqual(users.ID(), 1).One(ctx, db)
	if err != nil {
		fmt.Printf("failed to query user: %s\n", err)
		return
	}
	fmt.Printf("%d %q %q\n", user.ID, user.Email, user.Status)

	// Output:
	// 1 "" "pending"
}
