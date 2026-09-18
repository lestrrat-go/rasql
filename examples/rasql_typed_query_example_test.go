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
	// The generated create builder binds each fixture row's fields as values,
	// through the columns the generator bound for it.
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

	// Bind binds every users column to the table, and UsersProjection selects
	// them in the order the generated row type scans them.
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
	q := base.OrderBy(rasql.AscExpr(columns.Email.Expr()))
	q, err = q.Offset(1)
	if err != nil {
		fmt.Printf("failed to page users query: %s\n", err)
		return
	}
	q, err = q.Limit(2)
	if err != nil {
		fmt.Printf("failed to page users query: %s\n", err)
		return
	}

	// Rows yields decoded rows directly, so the loop does not need manual
	// scanning or conversion.
	// SQL: SELECT users.id, users.email, users.nickname, users.status, users.first_name, users.last_name FROM users ORDER BY users.email ASC LIMIT 2 OFFSET 1
	rows, err := rasql.Rows(ctx, db, q)
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
