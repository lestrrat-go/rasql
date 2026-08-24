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

func Example_rasql_typed_query() {
	// This example pages through several users and decodes them as
	// store.UsersRow values.
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
	// Use rasql.Insert for each fixture row so setup follows the public API.
	for _, user := range []store.UsersRow{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@example.com"},
	} {
		if _, err := rasql.Insert(ctx, db, users, user); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// SelectFrom knows the UsersRow result type from users. Query yields decoded
	// rows directly, so the loop does not need manual scanning or conversion.
	// SQL: SELECT users.id, users.email FROM users ORDER BY users.email ASC LIMIT 2 OFFSET 1
	rows, err := rasql.SelectFrom(users).
		OrderAsc(users.Email()).
		Offset(1).
		Limit(2).
		Query(ctx, db)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	for found, err := range rows {
		if err != nil {
			fmt.Printf("failed to query users: %s\n", err)
			return
		}
		fmt.Println(found.Email)
	}

	// Output:
	// bob@example.com
	// cyd@example.com
}
