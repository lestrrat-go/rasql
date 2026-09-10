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

func Example_rasql_delete() {
	// This example deletes rows by a typed predicate, then shows the rule that
	// keeps a dropped predicate from becoming a full-table delete by accident.
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
	for id, email := range map[int64]string{1: "ada@example.com", 2: "grace@example.com", 3: "edsger@example.com"} {
		plan := store.NewUsersCreate().ID(id).Email(email).FirstName("First").LastName("Last").Plan()
		if _, err := rasql.ExecMutation(ctx, executor, plan); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// NewDeletePlan takes a table and a typed predicate built through the
	// query package.
	// SQL: DELETE FROM users WHERE users.id = ? (argument: 1)
	byID, err := rasql.NewDeletePlan(users, query.EqualValue(users.ID(), int64(1)))
	if err != nil {
		fmt.Printf("failed to build delete: %s\n", err)
		return
	}
	outcome, err := rasql.ExecMutation(ctx, executor, byID)
	if err != nil {
		fmt.Printf("failed to delete user: %s\n", err)
		return
	}
	fmt.Printf("%d user deleted by id\n", outcome.Affected)

	// Where takes any predicate the query package can build.
	// SQL: DELETE FROM users WHERE users.id > ? (argument: 2)
	byPredicate, err := rasql.NewDeletePlan(users, query.GreaterValue(users.ID(), int64(2)))
	if err != nil {
		fmt.Printf("failed to build delete: %s\n", err)
		return
	}
	outcome, err = rasql.ExecMutation(ctx, executor, byPredicate)
	if err != nil {
		fmt.Printf("failed to delete users: %s\n", err)
		return
	}
	fmt.Printf("%d user deleted by predicate\n", outcome.Affected)

	// A zero predicate is rejected, so a dropped Where cannot become a
	// full-table delete by accident.
	if _, err := rasql.NewDeletePlan(users, query.Predicate{}); err != nil {
		fmt.Println(err)
	}

	// An unconditional delete has to say so explicitly at the portable
	// statement level, then adapt into the mutation API through
	// NewStatementPlan. Build renders it without executing it.
	// SQL: DELETE FROM users
	statement, err := query.NewDelete(users.Ref())
	if err != nil {
		fmt.Printf("failed to build delete: %s\n", err)
		return
	}
	statement, err = statement.AllowAll()
	if err != nil {
		fmt.Printf("failed to allow a full-table delete: %s\n", err)
		return
	}
	rendered, err := render.Delete(db.Dialect(), statement)
	if err != nil {
		fmt.Printf("failed to build delete: %s\n", err)
		return
	}
	fmt.Println(rendered.SQL())

	// Output:
	// 1 user deleted by id
	// 1 user deleted by predicate
	// rasql: delete plan requires a predicate
	// DELETE FROM "users"
}
