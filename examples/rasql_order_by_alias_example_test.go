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

// BEGIN(order_by_alias)

// Example_rasql_order_by_alias binds a projection to a variable once and
// passes that same variable to both Project and Order, so the ORDER BY reads
// the projection's already-computed result instead of repeating its
// expression, and renaming its alias can never drift the two apart the way
// writing the alias out as a second string could. displayName falls back
// from nickname to email with COALESCE.
func Example_rasql_order_by_alias() {
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
	users := store.Users()
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	nick := "Ada"
	for _, user := range []store.UsersRow{
		{ID: 1, Email: "ada@example.com", Nickname: &nick},
		{ID: 2, Email: "bob@example.com", Nickname: nil},
	} {
		if _, err := rasql.Insert(ctx, db, users, user); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// A local result type holds the decoded id and the aliased display name.
	// DisplayName has no rasql tag, so it maps to the alias by snake-casing
	// the field name to display_name.
	type userDisplayName struct {
		ID          int64
		DisplayName string
	}

	// displayName is written once and used in both Project and Order below.
	// SQL: SELECT users.id, COALESCE(users.nickname, users.email) AS display_name FROM users ORDER BY display_name DESC
	displayName := query.Coalesce(users.Nickname(), users.Email()).As("display_name")
	rows, err := rasql.DecodeFrom[userDisplayName](users).
		Project(users.ID(), displayName).
		Order(query.DescResult(displayName)).
		Query(ctx, db)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	for user, err := range rows {
		if err != nil {
			fmt.Printf("failed to read user: %s\n", err)
			return
		}
		fmt.Println(user.ID, user.DisplayName)
	}

	// A second builder orders by a projection whose result name two
	// projections report: id is projected without a wrapper, and nickname is
	// separately aliased id. rasql refuses this in Go rather than letting it
	// reach a server, since PostgreSQL and MySQL both call it ambiguous and
	// SQLite would otherwise resolve it silently.
	nicknameAsID := users.Nickname().As("id")
	_, err = rasql.DecodeFrom[userDisplayName](users).
		Project(users.ID(), nicknameAsID).
		Order(query.AscResult(nicknameAsID)).
		Build(dialect.SQLite())
	if err != nil {
		fmt.Println(err)
	}

	// Output:
	// 2 bob@example.com
	// 1 Ada
	// query: order_by[0]: orders by the result name "id", which 2 projections report, so the ordering is ambiguous; give one of them a distinct alias
}

// END(order_by_alias)
