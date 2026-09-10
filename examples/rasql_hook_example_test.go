package examples_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/query"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// Example_rasql_hook rejects a statement before it reaches database/sql. A
// Before hook sees the rendered SQL and its arguments and can refuse the
// operation; it cannot rewrite either, so what the hook inspects is what the
// driver would have run.
func Example_rasql_hook() {
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

	policy := rasql.HookFunc{
		BeforeFunc: func(ctx context.Context, operation rasql.Operation) error {
			if operation.Kind() == rasql.ExecOperation && operation.SQL() == `DELETE FROM "users"` {
				return errors.New("unfiltered deletes are disabled")
			}
			return nil
		},
	}

	db, err = db.WithHooks(policy)
	if err != nil {
		// Handle invalid hook configuration.
		fmt.Printf("failed to install the hook: %s\n", err)
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

	// AllowAll renders the full-table delete the hook is looking for, so the
	// hook refuses it and the statement never reaches the database.
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
	unfiltered, err := rasql.NewStatementPlan(statement)
	if err != nil {
		fmt.Printf("failed to adapt delete: %s\n", err)
		return
	}
	if _, err := rasql.ExecMutation(ctx, executor, unfiltered); err != nil {
		fmt.Println("refused:", err)
	}

	// A delete carrying a predicate renders different SQL, so the hook lets it through.
	filtered, err := rasql.NewDeletePlan(users, query.EqualValue(users.ID(), int64(1)))
	if err != nil {
		fmt.Printf("failed to build delete: %s\n", err)
		return
	}
	if _, err := rasql.ExecMutation(ctx, executor, filtered); err != nil {
		fmt.Printf("failed to delete user: %s\n", err)
		return
	}
	fmt.Println("filtered delete ran")

	// Output:
	// refused: rasql: hook before exec: unfiltered deletes are disabled
	// filtered delete ran
}
