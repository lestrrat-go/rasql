package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/query"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// Example_rasql_returning reads the row a RETURNING clause produces, which
// rasql.Exec cannot do because it discards result rows.
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

	// The insert names every column it has a value for. id is left to the
	// database and status to its column default, which is what this example
	// reads back.
	statement, err := query.NewInsert(users.Ref(),
		query.Set(users.Email(), "ada@example.com"),
		query.Set(users.FirstName(), "Ada"),
		query.Set(users.LastName(), "Lovelace"))
	if err != nil {
		fmt.Printf("failed to build insert: %s\n", err)
		return
	}

	// The RETURNING clause names all six columns rather than only the two the
	// database filled in. QueryWriteOne decodes into store.UsersRow, which maps
	// the whole users table, and it refuses a clause that omits a column of
	// that table: an omitted column would decode as a zero value with nothing
	// to say the database never sent it.
	statement, err = statement.WithReturning(users.ID(), users.Email(), users.Nickname(),
		users.Status(), users.FirstName(), users.LastName())
	if err != nil {
		fmt.Printf("failed to add RETURNING clause: %s\n", err)
		return
	}

	// SQL: INSERT INTO users (email, first_name, last_name) VALUES (?, ?, ?) RETURNING id, email, nickname, status, first_name, last_name (arguments: "ada@example.com", "Ada", "Lovelace")
	user, err := rasql.QueryWriteOne[store.UsersRow](ctx, db, statement)
	if err != nil {
		fmt.Printf("failed to query inserted user: %s\n", err)
		return
	}
	fmt.Printf("%d %q %q\n", user.ID, user.Email, user.Status)

	// Output:
	// 1 "ada@example.com" "pending"
}
