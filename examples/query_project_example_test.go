package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// Example_projectTypedResult changes what a query returns without rebuilding
// its source and filters.
func Example_projectTypedResult() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
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
	for _, user := range []store.UsersRow{
		{ID: 3, Email: "other@example.com"},
		{ID: 7, Email: "ada@example.com"},
	} {
		if _, err := store.Users().Create().ID(user.ID).Email(user.Email).FirstName("First").LastName("Last").Exec(ctx, db); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	usersProjection, err := store.UsersProjection(users)
	if err != nil {
		fmt.Printf("failed to build users projection: %s\n", err)
		return
	}
	// filteredUsers returns complete UsersRow values. The next operation needs
	// the same filtered records, but only their email addresses.
	filteredUsers := rasql.Select(users, usersProjection).
		Where(rasql.EqualValue(users.ID.Expr(), int64(7)))

	emailProjection, err := rasql.Scalar("email", users.Email.Expr(), schema.TextType{}, "")
	if err != nil {
		fmt.Printf("failed to build email projection: %s\n", err)
		return
	}
	// Building another Select would require repeating the source and WHERE
	// clause. Project reuses them and changes the result type from UsersRow to
	// string.
	emailQuery := rasql.Project(filteredUsers.Plan(), emailProjection)

	statement, err := rasql.Render(emailQuery, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to render statement: %s\n", err)
		return
	}
	fmt.Println(statement.SQL())

	email, err := rasql.One(ctx, db, emailQuery)
	if err != nil {
		fmt.Printf("failed to query user: %s\n", err)
		return
	}
	fmt.Println(email)
	// Output:
	// SELECT "users"."email" AS "email" FROM "users" WHERE ("users"."id" = ?)
	// ada@example.com
}
