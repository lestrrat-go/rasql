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

// Example_rasql_update solves a partial update where only one stored field
// should change. The generated patch builder emits that assignment with a
// typed predicate, and a low-level select reads the row back to verify it.
func Example_rasql_update() {
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
		fmt.Printf("failed to create executor: %s\n", err)
		return
	}
	users := store.Users()
	// Create the table described by the generated users descriptor.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	// Insert one row so the update has a persistent target. Exec plans and
	// runs the create in one call.
	if _, err := store.Users().Create().ID(42).Email("ada@example.com").FirstName("First").LastName("Last").Exec(ctx, db); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}
	// The generated patch builder writes only the fields named, and its
	// Where takes the typed predicate that matches the target row. Exec
	// plans and runs the patch in one call.
	// SQL: UPDATE users SET email = ? WHERE users.id = ? (arguments: "grace@example.com", 42)
	if _, err := store.Users().Patch().Email("grace@example.com").Where(rasql.EqualValue(users.ID.Expr(), int64(42))).Exec(ctx, db); err != nil {
		fmt.Printf("failed to update user: %s\n", err)
		return
	}

	// A dynamic SELECT reads back the row, through the same query and render
	// packages the typed layer builds on. query.NewSelect takes a
	// query.ColumnRef, and Column is the generated table's only way to
	// produce one.
	statement, err := query.NewSelect(users.Ref(), users.Ref().Column("id"), users.Ref().Column("email"))
	if err != nil {
		fmt.Printf("failed to build select: %s\n", err)
		return
	}
	statement, err = statement.WithWhere(query.Equal(users.Ref().Column("id"), 42))
	if err != nil {
		fmt.Printf("failed to filter select: %s\n", err)
		return
	}
	// SQL: SELECT users.id, users.email FROM users WHERE users.id = ? (argument: 42)
	rendered, err := render.Select(db.Dialect(), statement)
	if err != nil {
		fmt.Printf("failed to render select: %s\n", err)
		return
	}
	rows, err := db.QueryRendered(ctx, rendered)
	if err != nil {
		fmt.Printf("failed to query user: %s\n", err)
		return
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		fmt.Println("failed to query user: no rows")
		return
	}
	var id int64
	var email string
	if err := rows.Scan(&id, &email); err != nil {
		fmt.Printf("failed to read user: %s\n", err)
		return
	}
	if err := rows.Err(); err != nil {
		fmt.Printf("failed to read user: %s\n", err)
		return
	}
	fmt.Println(email)

	// Output:
	// grace@example.com
}
