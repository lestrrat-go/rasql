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

// Example_rasql_where_expressions filters and orders with the query package
// instead of the builder's own WhereEqual and OrderDesc, which is what a
// predicate richer than one comparison needs.
func Example_rasql_where_expressions() {
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
	for id, email := range map[int64]string{5: "ada@example.com", 15: "grace@example.com", 20: "edsger@example.com"} {
		if _, err := rasql.Insert(ctx, db, users, store.UsersRow{ID: id, Email: email}); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// Every plain value still travels as a bound argument: the constructor
	// binds it automatically, and the renderer turns it into the dialect's
	// placeholder.
	// SQL: SELECT users.id, users.email FROM users WHERE (users.id > ? AND users.id IS NOT NULL) ORDER BY users.id DESC (argument: 10)
	// BEGIN(where_expressions)
	rows, err := rasql.SelectFrom(users).
		Where(query.And(
			query.GreaterThan(users.ID(), 10),
			query.IsNotNull(users.ID()),
		)).
		Order(query.Desc(users.ID())).
		Query(ctx, db)
	// END(where_expressions)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	for user, err := range rows {
		if err != nil {
			fmt.Printf("failed to read user: %s\n", err)
			return
		}
		fmt.Println(user.ID, user.Email)
	}

	// Output:
	// 20 edsger@example.com
	// 15 grace@example.com
}
