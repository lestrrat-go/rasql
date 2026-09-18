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

func Example_rasql_sqlite_query() {
	// This example creates, inserts, and reads one generated row with SQLite.
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

	// store.Users() returns the generated table value, which carries the
	// descriptor and the row type.
	users := store.Users()

	// Create the schema described by the generated table descriptor.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	// The generated create builder binds the row's fields as values, through
	// the columns the generator bound for it.
	plan, err := store.Users().Create().ID(42).Email("ada@example.com").FirstName("First").LastName("Last").Plan()
	if err != nil {
		fmt.Printf("failed to build insert: %s\n", err)
		return
	}
	if _, err := rasql.ExecMutation(ctx, db, plan); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// The generated projection names one column per users field, so One
	// returns a decoded store.UsersRow.
	// SQL: SELECT users.id, users.email, users.nickname, users.status, users.first_name, users.last_name FROM users WHERE users.id = ? (argument: 42)
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
	user, err := rasql.One(ctx, db, base.Where(rasql.EqualValue(columns.ID.Expr(), int64(42))))
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}

	fmt.Println(user.Email)

	// Output:
	// ada@example.com
}
