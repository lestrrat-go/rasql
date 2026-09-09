package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
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
		query.Set(users.Email().Ref(), "ada@example.com"),
		query.Set(users.FirstName().Ref(), "Ada"),
		query.Set(users.LastName().Ref(), "Lovelace"))
	if err != nil {
		fmt.Printf("failed to build insert: %s\n", err)
		return
	}

	// The RETURNING clause names all six columns rather than only the two the
	// database filled in, so the generated row type's ScanRow can decode the
	// whole result in column order.
	statement, err = statement.WithReturning(users.ID().Ref(), users.Email().Ref(), users.Nickname().Ref(),
		users.Status().Ref(), users.FirstName().Ref(), users.LastName().Ref())
	if err != nil {
		fmt.Printf("failed to add RETURNING clause: %s\n", err)
		return
	}

	// SQL: INSERT INTO users (email, first_name, last_name) VALUES (?, ?, ?) RETURNING id, email, nickname, status, first_name, last_name (arguments: "ada@example.com", "Ada", "Lovelace")
	rendered, err := render.Insert(db.Dialect(), statement)
	if err != nil {
		fmt.Printf("failed to render insert: %s\n", err)
		return
	}
	rows, err := db.QueryRendered(ctx, rendered)
	if err != nil {
		fmt.Printf("failed to query inserted user: %s\n", err)
		return
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			fmt.Printf("failed to query inserted user: %s\n", err)
		} else {
			fmt.Println("failed to query inserted user: no rows")
		}
		return
	}
	var user store.UsersRow
	if err := user.ScanRow(rows); err != nil {
		fmt.Printf("failed to query inserted user: %s\n", err)
		return
	}
	if err := rows.Err(); err != nil {
		fmt.Printf("failed to query inserted user: %s\n", err)
		return
	}
	fmt.Printf("%d %q %q\n", user.ID, user.Email, user.Status)

	// Output:
	// 1 "ada@example.com" "pending"
}
