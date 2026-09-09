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

func Example_rasql_update() {
	// This example changes a generated row by using its primary-key field.
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
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 40, 0)
	if err != nil {
		fmt.Printf("failed to describe engine profile: %s\n", err)
		return
	}
	executor, err := rasql.AsExecutor(db, profile)
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
	// Insert one row so the update has a persistent target.
	createPlan := store.NewUsersCreate().ID(42).Email("ada@example.com").FirstName("First").LastName("Last").Plan()
	if _, err := rasql.ExecMutation(ctx, executor, createPlan); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// The generated patch builder writes only the fields named, and its
	// Where takes the typed predicate that matches the target row.
	// SQL: UPDATE users SET email = ? WHERE users.id = ? (arguments: "grace@example.com", 42)
	patchPlan, err := store.NewUsersPatch().Email("grace@example.com").Where(query.EqualValue(users.ID(), int64(42)))
	if err != nil {
		fmt.Printf("failed to build patch: %s\n", err)
		return
	}
	if _, err := rasql.ExecMutation(ctx, executor, patchPlan); err != nil {
		fmt.Printf("failed to update user: %s\n", err)
		return
	}

	// A dynamic SELECT reads back the row, through the same query and render
	// packages the typed layer builds on.
	statement, err := query.NewSelect(users.Ref(), users.ID().Ref(), users.Email().Ref())
	if err != nil {
		fmt.Printf("failed to build select: %s\n", err)
		return
	}
	statement, err = statement.WithWhere(query.Equal(users.ID().Ref(), 42))
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
