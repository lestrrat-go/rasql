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

// Example_rasql_debug_query renders a typed query's SQL with rasql.Render, which
// needs no database connection at all, and then runs the same query against a
// real database to show how many rows it returns.
func Example_rasql_debug_query() {
	users := store.Users()
	// Bind binds every users column to the table, and UsersProjection selects
	// them in the order the generated row type scans them.
	columns, err := (store.UsersColumns{}).Bind(users.Table())
	if err != nil {
		fmt.Printf("failed to bind users columns: %s\n", err)
		return
	}
	projection, err := store.UsersProjection(columns)
	if err != nil {
		fmt.Printf("failed to build users projection: %s\n", err)
		return
	}
	selected := rasql.Select(users, projection).
		Where(rasql.EqualValue(columns.ID.Expr(), int64(42)))

	// Render lowers the query to SQL text for a chosen dialect without
	// opening a database, which is exactly what inspecting a query before it
	// ever reaches a server needs.
	statement, err := rasql.Render(selected, dialect.PostgreSQL())
	if err != nil {
		fmt.Printf("failed to render statement: %s\n", err)
		return
	}
	fmt.Println(statement.SQL())
	fmt.Println(statement.Args())

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
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	// The table is empty, so a real database reports zero rows rather than
	// failing the way a fake one that answers every query with no columns at
	// all would.
	rows, err := rasql.All(ctx, db, selected)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	fmt.Printf("%d result rows\n", len(rows))

	// Output:
	// SELECT "users"."id" AS "id", "users"."email" AS "email", "users"."nickname" AS "nickname", "users"."status" AS "status", "users"."first_name" AS "first_name", "users"."last_name" AS "last_name" FROM "users" WHERE ("users"."id" = $1)
	// [42]
	// 0 result rows
}
