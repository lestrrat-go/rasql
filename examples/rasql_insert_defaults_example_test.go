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

// Example_rasql_insert_defaults writes a row whose id the database assigns and
// whose status comes from the column's default. The users table generated into
// examples/store declares status with that default.
func Example_rasql_insert_defaults() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	db, err := rasql.Open(ctx, database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create executor: %s\n", err)
		return
	}
	users := store.Users()
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	// Leaving off ID() lets the autoincrement primary key supply its own
	// value. DefaultStatus() asks the column's own default for status.
	// ClearNickname() writes SQL NULL explicitly rather than omitting the
	// column, and every other column is written from the value given.
	// SQL: INSERT INTO users (email, nickname, first_name, last_name) VALUES (?, ?, ?, ?) (arguments: "", NULL, "", "")
	plan, err := store.NewUsersCreate().Email("").ClearNickname().DefaultStatus().FirstName("").LastName("").Plan()
	if err != nil {
		fmt.Printf("failed to build insert: %s\n", err)
		return
	}
	if _, err := rasql.ExecMutation(ctx, db, plan); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// A dynamic SELECT reads back what the database actually assigned,
	// through the same query and render packages the typed layer builds on.
	// query.NewSelect takes a query.ColumnRef, and Column is the generated
	// table's only way to produce one.
	statement, err := query.NewSelect(users.Ref(), users.Column("id"), users.Column("email"), users.Column("status"))
	if err != nil {
		fmt.Printf("failed to build select: %s\n", err)
		return
	}
	statement, err = statement.WithWhere(query.Equal(users.Column("id"), 1))
	if err != nil {
		fmt.Printf("failed to filter select: %s\n", err)
		return
	}
	// SQL: SELECT users.id, users.email, users.status FROM users WHERE users.id = ? (argument: 1)
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
	var email, status string
	if err := rows.Scan(&id, &email, &status); err != nil {
		fmt.Printf("failed to read user: %s\n", err)
		return
	}
	if err := rows.Err(); err != nil {
		fmt.Printf("failed to read user: %s\n", err)
		return
	}
	fmt.Printf("%d %q %q\n", id, email, status)

	// Output:
	// 1 "" "pending"
}
