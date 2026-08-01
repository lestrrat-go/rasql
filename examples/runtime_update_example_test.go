package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/runtime"
)

func Example_runtime_update() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	client, err := runtime.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create runtime client: %s\n", err)
		return
	}
	if err := client.CreateTable(ctx, users.Ref().Table()); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if _, err := runtime.Insert(ctx, client, users, UserRow{ID: 42, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	id, err := users.Ref().Column("id")
	if err != nil {
		fmt.Printf("failed to find users.id: %s\n", err)
		return
	}
	email, err := users.Ref().Column("email")
	if err != nil {
		fmt.Printf("failed to find users.email: %s\n", err)
		return
	}
	// Write statements stay immutable. WithWhere returns the UPDATE statement
	// that client.Exec renders and executes with its bound values.
	update, err := query.NewUpdate(users.Ref(), query.Set(email, query.Bind("grace@example.com")))
	if err != nil {
		fmt.Printf("failed to build update: %s\n", err)
		return
	}
	update, err = update.WithWhere(query.Equal(id, query.Bind(42)))
	if err != nil {
		fmt.Printf("failed to add update predicate: %s\n", err)
		return
	}
	if _, err := client.Exec(ctx, update); err != nil {
		fmt.Printf("failed to update user: %s\n", err)
		return
	}

	user, err := runtime.SelectFrom(client, users).WhereEqual("id", 42).One(ctx)
	if err != nil {
		fmt.Printf("failed to query user: %s\n", err)
		return
	}
	fmt.Println(user.Email)

	// Output:
	// grace@example.com
}
