package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/query"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_scalar_function() {
	// This example looks a user up by email regardless of case with LOWER,
	// then reads every user's display name, falling back to their email with
	// COALESCE when no nickname is set. nickname is the users column declared
	// nullable, which is what gives COALESCE a real NULL to fall back from.
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
	// A local result type holds the decoded id and display name.
	type userName struct {
		ID   int64
		Name string
	}
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	nick := "Ada"
	for _, user := range []store.UsersRow{
		{ID: 1, Email: "Ada@Example.com", Nickname: &nick},
		{ID: 2, Email: "bob@example.com", Nickname: nil},
	} {
		if _, err := rasql.Insert(ctx, db, users, user); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// LOWER(email) matches "Ada@Example.com" against the lower-case literal a
	// caller would type, regardless of how the stored value was cased.
	// SQL: SELECT users.id, COALESCE(users.nickname, users.email) AS name FROM users WHERE LOWER(users.email) = ? (argument: "ada@example.com")
	byEmail, err := rasql.DecodeFrom[userName](users).
		Project(users.ID(), query.Coalesce(users.Nickname(), users.Email()).As("name")).
		Where(query.Equal(query.Lower(users.Email()), "ada@example.com")).
		Query(ctx, db)
	if err != nil {
		fmt.Printf("failed to query user by email: %s\n", err)
		return
	}
	for user, err := range byEmail {
		if err != nil {
			fmt.Printf("failed to query user by email: %s\n", err)
			return
		}
		fmt.Println(user.Name)
	}

	// COALESCE(nickname, email) reads every user's display name, falling
	// back to the email once nickname is NULL.
	// SQL: SELECT users.id, COALESCE(users.nickname, users.email) AS name FROM users ORDER BY users.id ASC
	names, err := rasql.DecodeFrom[userName](users).
		Project(users.ID(), query.Coalesce(users.Nickname(), users.Email()).As("name")).
		OrderAsc(users.ID()).
		Query(ctx, db)
	if err != nil {
		fmt.Printf("failed to query user names: %s\n", err)
		return
	}
	for user, err := range names {
		if err != nil {
			fmt.Printf("failed to query user names: %s\n", err)
			return
		}
		fmt.Println(user.ID, user.Name)
	}

	// Output:
	// Ada
	// 1 Ada
	// 2 bob@example.com
}
