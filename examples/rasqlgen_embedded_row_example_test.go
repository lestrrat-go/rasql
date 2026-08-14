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

// BEGIN(embedded_row)
type userWithRole struct {
	store.UsersRow // promotes ScanRow and ScanDestinations
	Role           string
}

// END(embedded_row)

// Example_rasqlgen_embedded_row shows what embedding a generated row type does
// to a read. The wrapper promotes the embedded row's scan methods, the typed
// read path uses them, and they fill only the embedded fields, so Role keeps
// its zero value and nothing is reported.
func Example_rasqlgen_embedded_row() {
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
	if err := rasql.CreateTable(ctx, db, store.Users()); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if _, err := rasql.Insert(ctx, db, store.Users(), store.UsersRow{ID: 1, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	users := store.Users()
	wrapped, err := rasql.DecodeFrom[userWithRole](users).
		Project(query.Project(users.ID()), query.Project(users.Email())).
		One(ctx, db)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	fmt.Printf("%d %q role=%q\n", wrapped.ID, wrapped.Email, wrapped.Role)

	// Output:
	// 1 "ada@example.com" role=""
}
