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

// Example_rasql_partial_update writes one column of several rows. rasql.Update
// writes a whole row by its primary key, so a statement built through the query
// package is what states which columns change and which rows they change on.
func Example_rasql_partial_update() {
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
	for id, email := range map[int64]string{42: "old@example.com", 512: "keep@example.com"} {
		if _, err := rasql.Insert(ctx, db, users, store.UsersRow{ID: id, Email: email}); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// SQL: UPDATE users SET email = ? WHERE users.id < ? (arguments: "ada@example.com", 100)
	// BEGIN(partial_update)
	statement, err := query.NewUpdate(users.Ref(), query.Set(users.Email(), "ada@example.com"))
	if err != nil {
		fmt.Printf("failed to build update: %s\n", err)
		return
	}
	statement, err = statement.WithWhere(query.LessThan(users.ID(), 100))
	if err != nil {
		fmt.Printf("failed to filter update: %s\n", err)
		return
	}
	result, err := rasql.Exec(ctx, db, statement)
	// END(partial_update)
	if err != nil {
		fmt.Printf("failed to run update: %s\n", err)
		return
	}
	updated, err := result.RowsAffected()
	if err != nil {
		fmt.Printf("failed to count updated users: %s\n", err)
		return
	}
	fmt.Printf("%d user updated\n", updated)

	// The row outside the predicate keeps the email it was inserted with.
	kept, err := rasql.SelectFrom(users).WhereEqual(users.ID(), 512).One(ctx, db)
	if err != nil {
		fmt.Printf("failed to query user: %s\n", err)
		return
	}
	fmt.Println(kept.Email)

	// Output:
	// 1 user updated
	// keep@example.com
}
