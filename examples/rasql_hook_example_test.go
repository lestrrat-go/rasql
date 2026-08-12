package examples_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
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
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	// BEGIN(hook)
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
	// END(hook)

	// AllowAll renders the full-table delete the hook is looking for, so the
	// hook refuses it and the statement never reaches the database.
	if _, err := rasql.DeleteFrom(users).AllowAll().Exec(ctx, db); err != nil {
		fmt.Println("refused:", err)
	}

	// A delete carrying a predicate renders different SQL, so the hook lets it through.
	if _, err := rasql.DeleteFrom(users).WhereEqual(users.ID, 1).Exec(ctx, db); err != nil {
		fmt.Printf("failed to delete user: %s\n", err)
		return
	}
	fmt.Println("filtered delete ran")

	// Output:
	// refused: rasql: hook before exec: unfiltered deletes are disabled
	// filtered delete ran
}
