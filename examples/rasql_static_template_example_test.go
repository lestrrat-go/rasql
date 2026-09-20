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

// Example_rasql_static_template solves the need to execute reviewed SQL with a
// runtime value while keeping that value out of the SQL text. namedsql parses
// and compiles a restricted template, Bind supplies its argument, and
// QueryRendered returns database/sql rows for manual scanning.
func Example_rasql_static_template() {
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
	db, err := rasql.Open(ctx, database, dialect.SQLite())
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
	// Insert a row that the bound template will find. query.Set takes a
	// query.ColumnRef, and Column is the generated table's only way to
	// produce one.
	insert, err := query.NewInsert(users.Ref(),
		query.Set(users.Ref().Column("id"), int64(42)), query.Set(users.Ref().Column("email"), "ada@example.com"),
		query.Set(users.Ref().Column("first_name"), "Ada"), query.Set(users.Ref().Column("last_name"), "Lovelace"))
	if err != nil {
		fmt.Printf("failed to build insert: %s\n", err)
		return
	}
	renderedInsert, err := render.Insert(db.Dialect(), insert)
	if err != nil {
		fmt.Printf("failed to render insert: %s\n", err)
		return
	}
	if _, err := db.Exec(ctx, renderedInsert); err != nil {
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
