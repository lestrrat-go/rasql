package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/dynamic"
	"github.com/lestrrat-go/rasql/query"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// Example_rasql_delete_returning reads the rows a delete removes. The fluent
// delete offers both terminals for that: Query hands back dynamic rows, and
// QueryDeleteOne decodes one row into a Go type. Each builder runs one of them.
func Example_rasql_delete_returning() {
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
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	for id, email := range map[int64]string{42: "ada@example.com", 43: "grace@example.com"} {
		if _, err := rasql.Insert(ctx, db, users, UserRow{ID: id, Email: email}); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// SQL: DELETE FROM users WHERE users.id = ? RETURNING id, email (argument: 42)
	// BEGIN(delete_returning_dynamic)
	builder := dynamic.DeleteFrom(users.Ref()).
		WhereEqual(users.ID, 42).
		Returning(query.Project(users.ID), query.Project(users.Email))

	rows, err := builder.Query(ctx, db)
	// END(delete_returning_dynamic)
	if err != nil {
		fmt.Printf("failed to delete user: %s\n", err)
		return
	}
	for deleted, err := range rows {
		if err != nil {
			fmt.Printf("failed to read deleted user: %s\n", err)
			return
		}
		var email string
		if err := dynamic.Assign(deleted, "email", &email); err != nil {
			fmt.Printf("failed to read the email column: %s\n", err)
			return
		}
		fmt.Println("dynamic:", email)
	}

	// SQL: DELETE FROM users WHERE users.id = ? RETURNING id, email (argument: 43)
	// BEGIN(delete_returning_typed)
	typed := rasql.DeleteFrom(users).
		WhereEqual(users.ID, 43).
		Returning(query.Project(users.ID), query.Project(users.Email))

	deleted, err := rasql.QueryDeleteOne[UserRow](ctx, db, typed)
	// END(delete_returning_typed)
	if err != nil {
		fmt.Printf("failed to delete user: %s\n", err)
		return
	}
	fmt.Println("typed:", deleted.ID, deleted.Email)

	// Output:
	// dynamic: ada@example.com
	// typed: 43 grace@example.com
}
