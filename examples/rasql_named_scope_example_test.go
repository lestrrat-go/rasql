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

// namedScopeUsersQuery builds the canonical Query[store.UsersRow] that projects
// every users column, and returns the bound columns so a caller can filter or
// order by them.
func namedScopeUsersQuery() (rasql.Query[store.UsersRow], store.UsersExpressions, error) {
	users := store.Users()
	columns, err := (store.UsersColumns{}).Bind(users)
	if err != nil {
		return rasql.Query[store.UsersRow]{}, store.UsersExpressions{}, err
	}
	projection, err := store.UsersProjection(columns)
	if err != nil {
		return rasql.Query[store.UsersRow]{}, store.UsersExpressions{}, err
	}
	return rasql.Select(users, projection), columns, nil
}

type namedScopeUsersScope interface {
	Apply(rasql.Query[store.UsersRow]) rasql.Query[store.UsersRow]
}

type namedScopeActive struct{ columns store.UsersExpressions }

func (s namedScopeActive) Apply(q rasql.Query[store.UsersRow]) rasql.Query[store.UsersRow] {
	return q.Where(rasql.EqualValue(s.columns.Status.Expr(), "active"))
}

type namedScopeSurnamed struct {
	columns store.UsersExpressions
	surname string
}

func (s namedScopeSurnamed) Apply(q rasql.Query[store.UsersRow]) rasql.Query[store.UsersRow] {
	return q.Where(rasql.EqualValue(s.columns.LastName.Expr(), s.surname))
}

type namedScopeByEmail struct{ columns store.UsersExpressions }

func (s namedScopeByEmail) Apply(q rasql.Query[store.UsersRow]) rasql.Query[store.UsersRow] {
	return q.OrderBy(rasql.AscExpr(s.columns.Email.Expr()))
}

func namedScopeApply(q rasql.Query[store.UsersRow], scopes ...namedScopeUsersScope) rasql.Query[store.UsersRow] {
	for _, scope := range scopes {
		q = scope.Apply(q)
	}
	return q
}

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

	base, columns, err := namedScopeUsersQuery()
	if err != nil {
		fmt.Printf("failed to build users query: %s\n", err)
		return
	}
	active := namedScopeActive{columns: columns}
	byEmail := namedScopeByEmail{columns: columns}

	hoppers, err := rasql.All(ctx, db,
		namedScopeApply(base, active, namedScopeSurnamed{columns: columns, surname: "Hopper"}, byEmail))
	if err != nil {
		fmt.Printf("failed to query active Hoppers: %s\n", err)
		return
	}
	for _, found := range hoppers {
		fmt.Printf("active Hopper: %s\n", found.Email)
	}

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
