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
// whose status comes from the column's default. default_users is generated into
// examples/store from a descriptor that declares that default.
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
	defaultUsers := store.DefaultUsers()
	if err := rasql.CreateTable(ctx, db, defaultUsers); err != nil {
		fmt.Printf("failed to create default_users table: %s\n", err)
		return
	}

	// Name each database-assigned column. Email remains an explicit empty string.
	// SQL: INSERT INTO default_users (email) VALUES (?) (argument: "")
	if _, err := rasql.InsertWithOptions(ctx, db, defaultUsers, store.DefaultUsersRow{}, rasql.DefaultColumns("id", "status")); err != nil {
		fmt.Printf("failed to insert default user: %s\n", err)
		return
	}

	// SQL: SELECT default_users.id, default_users.email, default_users.status FROM default_users WHERE default_users.id = ? (argument: 1)
	user, err := rasql.SelectFrom(defaultUsers).WhereEqual(defaultUsers.ID(), 1).One(ctx, db)
	if err != nil {
		fmt.Printf("failed to query default user: %s\n", err)
		return
	}
	fmt.Printf("%d %q %q\n", user.ID, user.Email, user.Status)

	// Output:
	// 1 "" "pending"
}
