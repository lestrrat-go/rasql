package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_count() {
	// This example counts rows matched by a query, without decoding any of
	// them, using a COUNT(*) projection.
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
	// The generated create builder binds each fixture row's fields as values.
	for _, user := range []store.UsersRow{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@example.com"},
	} {
		if _, err := store.Users().Create().ID(user.ID).Email(user.Email).FirstName("First").LastName("Last").Exec(ctx, db); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	columns, err := (store.UsersColumns{}).Bind(users)
	if err != nil {
		fmt.Printf("failed to bind users columns: %s\n", err)
		return
	}
	countProjection, err := rasql.Scalar("count", rasql.CountRows(), schema.IntegerType{}, "")
	if err != nil {
		fmt.Printf("failed to build count projection: %s\n", err)
		return
	}
	base := rasql.Select(users, countProjection)

	// One decodes the single COUNT(*) result, never a matched row.
	// SQL: SELECT COUNT(*) AS count FROM users
	total, err := rasql.One(ctx, db, base)
	if err != nil {
		fmt.Printf("failed to count users: %s\n", err)
		return
	}
	fmt.Println("total:", total)

	// SQL: SELECT COUNT(*) AS count FROM users WHERE users.id = ? (argument: 2)
	filtered, err := rasql.One(ctx, db, base.Where(rasql.EqualValue(columns.ID.Expr(), int64(2))))
	if err != nil {
		fmt.Printf("failed to count filtered users: %s\n", err)
		return
	}
	fmt.Println("filtered:", filtered)

	// Output:
	// total: 3
	// filtered: 1
}
