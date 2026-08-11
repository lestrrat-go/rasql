package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_count() {
	// This example counts rows matched by a builder without paging through them.
	// users and UserRow are declared in query_example_tables_test.go with the
	// shape rasqlgen emits; an application that generated into package store
	// would write store.Users() and store.UsersRow instead.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A Client couples a database handle with the dialect used to render SQL.
	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}
	// Create the table described by the generated users descriptor.
	if err := rasql.Create(ctx, client, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	// Use rasql.Insert for each fixture row so setup follows the public API.
	for _, user := range []UserRow{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@example.com"},
	} {
		if _, err := rasql.Insert(ctx, client, users, user); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// Count runs COUNT(*) over the builder's WHERE and joins, without decoding
	// any row into a UserRow. It rejects a builder with Limit or Offset set,
	// since a count of a paged statement is not the count the caller asked for.
	// SQL: SELECT COUNT(*) FROM users
	total, err := rasql.SelectFrom(users).Count(ctx, client)
	if err != nil {
		fmt.Printf("failed to count users: %s\n", err)
		return
	}
	fmt.Println("total:", total)

	// SQL: SELECT COUNT(*) FROM users WHERE users.id = ? (argument: 2)
	filtered, err := rasql.SelectFrom(users).WhereEqual(users.ID, 2).Count(ctx, client)
	if err != nil {
		fmt.Printf("failed to count filtered users: %s\n", err)
		return
	}
	fmt.Println("filtered:", filtered)

	// Output:
	// total: 3
	// filtered: 1
}
