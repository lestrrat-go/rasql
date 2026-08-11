package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_quickstart() {
	// This example runs one insert, read, update, and delete against a table
	// descriptor. users and UserRow are declared in query_example_tables_test.go
	// with the shape rasqlgen emits; an application that generated into package
	// store would write store.Users() and store.UsersRow instead.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A Client couples a database handle with the dialect used to render SQL.
	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}
	// A real application creates its tables through migrations instead.
	if err := rasql.CreateTable(ctx, client, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	// Insert writes the tagged fields of UserRow as bound values.
	// SQL: INSERT INTO users (id, email) VALUES (?, ?) (arguments: 1, "ada@example.com")
	if _, err := rasql.Insert(ctx, client, users, UserRow{ID: 1, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// One returns a single decoded row and fails when the result holds any other count.
	// SQL: SELECT users.id, users.email FROM users WHERE users.id = ? (argument: 1)
	user, err := rasql.SelectFrom(users).WhereEqual(users.ID, 1).One(ctx, client)
	if err != nil {
		fmt.Printf("failed to query user: %s\n", err)
		return
	}
	fmt.Println(user.Email)

	// Update matches the row's primary key and writes its remaining fields.
	// SQL: UPDATE users SET email = ? WHERE id = ? (arguments: "grace@example.com", 1)
	if _, err := rasql.Update(ctx, client, users, UserRow{ID: 1, Email: "grace@example.com"}); err != nil {
		fmt.Printf("failed to update user: %s\n", err)
		return
	}

	// All collects every result row instead of ranging over them.
	// SQL: SELECT users.id, users.email FROM users ORDER BY users.id ASC
	found, err := rasql.SelectFrom(users).OrderAsc(users.ID).All(ctx, client)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	fmt.Println(len(found), found[0].Email)

	// DeleteFrom builds the predicate from generated columns, like the select builder.
	// SQL: DELETE FROM users WHERE users.id = ? (argument: 1)
	result, err := rasql.DeleteFrom(users).WhereEqual(users.ID, 1).Exec(ctx, client)
	if err != nil {
		fmt.Printf("failed to delete user: %s\n", err)
		return
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		fmt.Printf("failed to count deleted users: %s\n", err)
		return
	}
	fmt.Printf("%d user deleted\n", deleted)

	// Output:
	// ada@example.com
	// 1 grace@example.com
	// 1 user deleted
}
