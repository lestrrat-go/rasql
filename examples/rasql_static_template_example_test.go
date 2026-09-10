package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/namedsql"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_static_template() {
	// This example binds a static template and executes it through rasql.DB.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A DB couples a database handle with the dialect used to render SQL.
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	users := store.Users()
	// Create the table described by the generated users descriptor.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	// Insert a row that the bound template will find.
	insert, err := query.NewInsert(users.Ref(),
		query.Set(users.ID().Ref(), int64(42)), query.Set(users.Email().Ref(), "ada@example.com"),
		query.Set(users.FirstName().Ref(), "Ada"), query.Set(users.LastName().Ref(), "Lovelace"))
	if err != nil {
		fmt.Printf("failed to build insert: %s\n", err)
		return
	}
	renderedInsert, err := render.Insert(db.Dialect(), insert)
	if err != nil {
		fmt.Printf("failed to render insert: %s\n", err)
		return
	}
	if _, err := db.ExecRendered(ctx, renderedInsert); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// Parse accepts only SQL text and named bind actions.
	parsed, err := namedsql.Parse("user_by_email", "SELECT id, email FROM users WHERE email = {{bind \"email\"}}")
	if err != nil {
		fmt.Printf("failed to parse template: %s\n", err)
		return
	}
	// Compile converts named binds into the selected dialect's placeholders.
	compiled, err := parsed.Compile(dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to compile template: %s\n", err)
		return
	}
	// Bind supplies values without putting them into the SQL text.
	statement, err := compiled.Bind(map[string]any{"email": "ada@example.com"})
	if err != nil {
		fmt.Printf("failed to bind template: %s\n", err)
		return
	}

	// SQL: SELECT id, email FROM users WHERE email = ? (argument: "ada@example.com")
	// QueryRendered runs the template statement and returns its database/sql rows.
	sqlRows, err := db.QueryRendered(ctx, statement)
	if err != nil {
		fmt.Printf("failed to query user: %s\n", err)
		return
	}
	defer func() { _ = sqlRows.Close() }()
	for sqlRows.Next() {
		var id int64
		var email string
		if err := sqlRows.Scan(&id, &email); err != nil {
			fmt.Printf("failed to read user: %s\n", err)
			return
		}
		fmt.Println(email)
	}
	if err := sqlRows.Err(); err != nil {
		fmt.Printf("failed to read user: %s\n", err)
		return
	}

	// Output:
	// ada@example.com
}
