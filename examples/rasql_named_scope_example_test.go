package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_named_scope() {
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
		{ID: 1, Email: "ada@example.com", Status: "active", FirstName: "Ada", LastName: "Lovelace"},
		{ID: 2, Email: "bob@example.com", Status: "pending", FirstName: "Bob", LastName: "Hopper"},
		{ID: 3, Email: "cyd@example.com", Status: "active", FirstName: "Cyd", LastName: "Hopper"},
		{ID: 4, Email: "dee@example.com", Status: "active", FirstName: "Dee", LastName: "Hopper"},
	} {
		if _, err := users.Create().
			ID(user.ID).
			Email(user.Email).
			Status(user.Status).
			FirstName(user.FirstName).
			LastName(user.LastName).
			Exec(ctx, db); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// The base query selects every user, with no predicate and no ordering.
	projection, err := store.UsersProjection(users)
	if err != nil {
		fmt.Printf("failed to build users projection: %s\n", err)
		return
	}
	base := rasql.Select(users, projection)

	// ActiveRecord registers a named scope such as
	// `scope :active, -> { where(status: "active") }`. In Go, each scope below
	// is a function that receives a users query and returns a new users query.
	// Naming that function signature usersScope lets applyScopes accept several
	// scopes and lets surnamed declare that it returns one, without repeating the
	// full signature. rasql does not require or register this local type, and it
	// is unrelated to rasql.Scope, which rasql.Within uses for transaction work.
	type usersScope func(rasql.Query[store.UsersRow]) rasql.Query[store.UsersRow]

	// active adds WHERE users.status = ? and binds "active". Where returns the
	// changed query without modifying the query passed to the scope.
	active := usersScope(func(q rasql.Query[store.UsersRow]) rasql.Query[store.UsersRow] {
		return q.Where(rasql.EqualValue(users.Status.Expr(), "active"))
	})

	// surnamed is a parameterized scope. Calling surnamed("Hopper") returns a
	// scope that adds WHERE users.last_name = ? and binds "Hopper".
	surnamed := func(surname string) usersScope {
		return func(q rasql.Query[store.UsersRow]) rasql.Query[store.UsersRow] {
			return q.Where(rasql.EqualValue(users.LastName.Expr(), surname))
		}
	}

	// byEmail orders by users.email in ascending order without adding a filter.
	byEmail := usersScope(func(q rasql.Query[store.UsersRow]) rasql.Query[store.UsersRow] {
		return q.OrderBy(rasql.AscExpr(users.Email.Expr()))
	})

	// applyScopes passes the result of each scope to the next scope. Applying
	// active, surnamed("Hopper"), and byEmail therefore adds both WHERE
	// predicates with AND and then adds the email ordering. base remains unchanged.
	applyScopes := func(q rasql.Query[store.UsersRow], scopes ...usersScope) rasql.Query[store.UsersRow] {
		for _, scope := range scopes {
			q = scope(q)
		}
		return q
	}

	// SQL: SELECT every users column FROM "users"
	//      WHERE (("users"."status" = ?) AND ("users"."last_name" = ?))
	//      ORDER BY "users"."email"   (arguments: active, Hopper)
	hoppers, err := rasql.All(ctx, db,
		applyScopes(base, active, surnamed("Hopper"), byEmail))
	if err != nil {
		fmt.Printf("failed to query active Hoppers: %s\n", err)
		return
	}
	for _, found := range hoppers {
		fmt.Printf("active Hopper: %s\n", found.Email)
	}

	// base still selects every row, because no scope changed it. Dropping the
	// surname scope reaches the fourth active user again.
	// SQL: SELECT every users column FROM "users"
	//      WHERE ("users"."status" = ?)
	//      ORDER BY "users"."email"   (arguments: active)
	everyone, err := rasql.All(ctx, db, applyScopes(base, active, byEmail))
	if err != nil {
		fmt.Printf("failed to query active users: %s\n", err)
		return
	}
	for _, found := range everyone {
		fmt.Printf("active: %s\n", found.Email)
	}

	// Output:
	// active Hopper: cyd@example.com
	// active Hopper: dee@example.com
	// active: ada@example.com
	// active: cyd@example.com
	// active: dee@example.com
}
