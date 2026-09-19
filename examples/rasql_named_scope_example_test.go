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

// namedScopeUsersQuery builds the base query every scope below starts from: a
// Query[store.UsersRow] projecting every users column, unfiltered and unordered.
//
// It returns the table beside it because a scope needs its columns. A predicate
// is built from a column bound to one source, not from a column name, so a caller
// that wants to filter on status has to hold the same store.UsersTable this query
// was projected from.
func namedScopeUsersQuery() (rasql.Query[store.UsersRow], store.UsersTable, error) {
	users := store.Users()
	projection, err := store.UsersProjection(users)
	if err != nil {
		return rasql.Query[store.UsersRow]{}, store.UsersTable{}, err
	}
	return rasql.Select(users, projection), users, nil
}

// namedScopeUsersScope is one named, reusable piece of a users query. Ruby's
// ActiveRecord calls this a scope and registers it on the model, as
// `scope :active, -> { where(status: "active") }`; rasql has no such registry,
// so a scope here is an ordinary Go value that the caller writes and names.
//
// Apply takes a query and returns a query, which is what lets two scopes chain
// in either order: rasql.Query is immutable, so every builder method returns a
// new value and the query handed to Apply is left as it was.
//
// A rasql.Scope is an unrelated thing. It is the transaction or savepoint
// callback rasql.Within runs.
type namedScopeUsersScope interface {
	Apply(rasql.Query[store.UsersRow]) rasql.Query[store.UsersRow]
}

// namedScopeActive keeps the rows whose status column is "active".
type namedScopeActive struct{ users store.UsersTable }

func (s namedScopeActive) Apply(q rasql.Query[store.UsersRow]) rasql.Query[store.UsersRow] {
	return q.Where(rasql.EqualValue(s.users.Status.Expr(), "active"))
}

// namedScopeSurnamed keeps the rows whose last_name column equals the surname
// the caller gives, the way an ActiveRecord scope takes a lambda argument.
type namedScopeSurnamed struct {
	users   store.UsersTable
	surname string
}

func (s namedScopeSurnamed) Apply(q rasql.Query[store.UsersRow]) rasql.Query[store.UsersRow] {
	return q.Where(rasql.EqualValue(s.users.LastName.Expr(), s.surname))
}

// namedScopeByEmail sorts the rows by the email column, ascending. A scope adds
// an ordering as readily as a predicate, since both are builder methods on the
// same query.
type namedScopeByEmail struct{ users store.UsersTable }

func (s namedScopeByEmail) Apply(q rasql.Query[store.UsersRow]) rasql.Query[store.UsersRow] {
	return q.OrderBy(rasql.AscExpr(s.users.Email.Expr()))
}

// namedScopeApply applies each scope to the query in turn, left to right, which
// spells out what `Users.active.surnamed("Hopper").by_email` chains together in
// ActiveRecord. Two Where calls are combined with AND, so the order of the
// scopes changes nothing about the rows that come back.
func namedScopeApply(q rasql.Query[store.UsersRow], scopes ...namedScopeUsersScope) rasql.Query[store.UsersRow] {
	for _, scope := range scopes {
		q = scope.Apply(q)
	}
	return q
}

func Example_rasql_named_scope() {
	// This example names two filters and one ordering, then combines them two
	// ways against a single base query.
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

	// base selects every user, with no predicate and no ordering. scoped is the
	// table that query was projected from, and its column fields are what each
	// scope builds its predicate from.
	base, scoped, err := namedScopeUsersQuery()
	if err != nil {
		fmt.Printf("failed to build users query: %s\n", err)
		return
	}
	// Name the two scopes this example reuses. Neither has touched base yet; a
	// scope is inert until Apply hands it a query.
	active := namedScopeActive{users: scoped}
	byEmail := namedScopeByEmail{users: scoped}

	// SQL: SELECT every users column FROM "users"
	//      WHERE (("users"."status" = ?) AND ("users"."last_name" = ?))
	//      ORDER BY "users"."email"   (arguments: active, Hopper)
	hoppers, err := rasql.All(ctx, db,
		namedScopeApply(base, active, namedScopeSurnamed{users: scoped, surname: "Hopper"}, byEmail))
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
	everyone, err := rasql.All(ctx, db, namedScopeApply(base, active, byEmail))
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
