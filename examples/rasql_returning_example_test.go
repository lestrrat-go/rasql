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

// Example_rasql_returning reads the row a RETURNING clause produces, which a
// mutation plan executed through ExecMutation cannot do because ExecMutation
// rejects a statement carrying RETURNING projections outright.
func Example_rasql_returning() {
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
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	users := store.Users()
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	// The insert names every column it has a value for. id is left to the
	// database and status to its column default, which is what this example
	// reads back.
	plan, err := store.NewUsersCreate().Email("ada@example.com").FirstName("Ada").LastName("Lovelace").Plan()
	if err != nil {
		fmt.Printf("failed to build insert: %s\n", err)
		return
	}

	// The generated projection names all six columns rather than only the two
	// the database filled in, so the RETURNING list supplies every field the
	// generated row type decodes.
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

	// Returning turns the mutation plan into a Query, so the saved row comes
	// back through the same One terminal every read uses.
	// SQL: INSERT INTO users (email, first_name, last_name) VALUES (?, ?, ?) RETURNING id, email, nickname, status, first_name, last_name (arguments: "ada@example.com", "Ada", "Lovelace")
	saved, err := rasql.Returning(plan, projection)
	if err != nil {
		fmt.Printf("failed to attach the RETURNING clause: %s\n", err)
		return
	}
	user, err := rasql.One(ctx, db, saved)
	if err != nil {
		fmt.Printf("failed to query inserted user: %s\n", err)
		return
	}
	fmt.Printf("%d %q %q\n", user.ID, user.Email, user.Status)

	// Output:
	// 1 "ada@example.com" "pending"
}
