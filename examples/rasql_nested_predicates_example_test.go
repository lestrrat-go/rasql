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

// Example_rasql_nested_predicates builds a predicate tree several levels deep
// and shows the SQL it renders, which is what a filter that mixes AND and OR
// needs. query.And and query.Or take expressions and return one, so either
// holds the other to any depth.
func Example_rasql_nested_predicates() {
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
	for _, user := range []UserRow{
		{ID: 5, Email: "ada@example.com"},
		{ID: 7, Email: "linus@other.org"},
		{ID: 15, Email: "grace@example.com"},
		{ID: 25, Email: "alan@example.com"},
	} {
		if _, err := rasql.Insert(ctx, db, users, user); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// The inner query.And sits inside a query.Or, which sits inside the Where
	// call, and the whole tree is one predicate. The builder is immutable, so
	// the same value below renders the statement and then runs it.
	selected := rasql.SelectFrom(users).
		Where(query.Like(users.Email(), "%@example.com")).
		Where(query.Or(
			query.LessThan(users.ID(), 10),
			query.And(
				query.GreaterThan(users.ID(), 20),
				query.IsNotNull(users.Email()),
			),
		)).
		Order(query.Asc(users.ID()))

	// Every level of the tree renders its own parentheses, so the SQL groups the
	// way the Go code nests rather than by the database's operator precedence.
	statement, err := selected.Build(db.Dialect())
	if err != nil {
		fmt.Printf("failed to build statement: %s\n", err)
		return
	}
	fmt.Println(statement.SQL())

	found, err := selected.All(ctx, db)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	for _, user := range found {
		fmt.Println(user.ID, user.Email)
	}

	// Output:
	// SELECT "users"."id", "users"."email" FROM "users" WHERE (("users"."email" LIKE ?) AND (("users"."id" < ?) OR (("users"."id" > ?) AND ("users"."email" IS NOT NULL)))) ORDER BY "users"."id"
	// 5 ada@example.com
	// 25 alan@example.com
}
