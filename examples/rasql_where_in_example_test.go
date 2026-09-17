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

func Example_rasql_where_in() {
	// This example selects rows whose id is one of a fixed set of values.
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
	for _, user := range []store.UsersRow{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@example.com"},
	} {
		plan, err := store.NewUsersCreate().ID(user.ID).Email(user.Email).FirstName("First").LastName("Last").Plan()
		if err != nil {
			fmt.Printf("failed to build insert: %s\n", err)
			return
		}
		if _, err := rasql.ExecMutation(ctx, db, plan); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// Bind binds every users column to the table, and UsersProjection selects
	// them in the order the generated row type scans them.
	columns, err := (store.UsersColumns{}).Bind(users.Table)
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

	// InValues binds one placeholder per value and skips the users whose id is
	// not in the list. It takes the first value separately so an empty IN
	// list, which is not legal SQL, cannot be written at all.
	// SQL: SELECT users.id, users.email, users.nickname, users.status, users.first_name, users.last_name FROM users WHERE users.id IN (?, ?) ORDER BY users.id ASC (arguments: 1, 3)
	rows, err := rasql.All(ctx, db,
		base.Where(rasql.InValues(columns.ID.Expr(), int64(1), int64(3))).
			OrderBy(rasql.AscExpr(columns.ID.Expr())))
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	for _, found := range rows {
		fmt.Println(found.Email)
	}

	// Output:
	// ada@example.com
	// cyd@example.com
}
