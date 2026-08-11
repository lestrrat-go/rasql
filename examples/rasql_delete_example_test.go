package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_delete() {
	// This example deletes rows by a generated column and by a query expression.
	// users and UserRow are declared in query_example_tables_test.go with the
	// shape rasqlgen emits; an application that generated into package store
	// would write store.Users() and store.UsersRow instead.
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
	// Create the table described by the generated users descriptor.
	if err := rasql.CreateTable(ctx, client, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	for id, email := range map[int64]string{1: "ada@example.com", 2: "grace@example.com", 3: "edsger@example.com"} {
		if _, err := rasql.Insert(ctx, client, users, UserRow{ID: id, Email: email}); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// WhereEqual takes a column of the target table and binds the value.
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
	fmt.Printf("%d user deleted by id\n", deleted)

	// Where takes any predicate built through the query package.
	// SQL: DELETE FROM users WHERE users.id > ? (argument: 2)
	result, err = rasql.DeleteFrom(users).Where(query.GreaterThan(users.ID, query.Bind(2))).Exec(ctx, client)
	if err != nil {
		fmt.Printf("failed to delete users: %s\n", err)
		return
	}
	deleted, err = result.RowsAffected()
	if err != nil {
		fmt.Printf("failed to count deleted users: %s\n", err)
		return
	}
	fmt.Printf("%d user deleted by predicate\n", deleted)

	// A builder with no predicate is rejected, so a dropped Where cannot become
	// a full-table delete by accident.
	if _, err := rasql.DeleteFrom(users).Build(client.Dialect()); err != nil {
		fmt.Println(err)
	}

	// AllowAll states the full-table delete. Build renders it without executing it.
	// SQL: DELETE FROM users
	statement, err := rasql.DeleteFrom(users).AllowAll().Build(client.Dialect())
	if err != nil {
		fmt.Printf("failed to build delete: %s\n", err)
		return
	}
	fmt.Println(statement.SQL())

	// Output:
	// 1 user deleted by id
	// 1 user deleted by predicate
	// rasql: DELETE requires a WHERE predicate or an explicit AllowAll
	// DELETE FROM "users"
}
