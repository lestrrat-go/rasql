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

// Example_rasql_transaction solves the need to run the same typed mutations
// and queries both inside and outside a transaction. Begin returns another
// rasql.DB, so callers pass the transactional value to existing operations and
// switch back to the original DB after Commit.
func Example_rasql_transaction() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// Open pairs the handle with the dialect used to render SQL and asks the
	// server its version, resolving the engine profile a DB needs to run
	// queries and mutations.
	db, err := rasql.Open(ctx, database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create executor: %s\n", err)
		return
	}
	users := store.Users()
	// Create the table before any transaction starts.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	// db.Begin starts a transaction on the same handle and returns another DB
	// bound to it, carrying the engine profile db already resolved. There is no
	// separate transaction type to carry around: tx is a DB, and it already
	// takes exactly the plans and queries db takes.
	tx, err := db.Begin(ctx, nil)
	if err != nil {
		fmt.Printf("failed to begin transaction: %s\n", err)
		return
	}
	// Rollback reports nothing once Commit has already succeeded, which is what
	// makes this bare defer correct rather than an error every caller discards.
	defer func() { _ = tx.Rollback() }()
	txExecutor := tx

	// SQL: INSERT INTO users (id, email) VALUES (?, ?) (arguments: 1, "ada@example.com")
	if _, err := store.Users().Create().ID(1).Email("ada@example.com").FirstName("First").LastName("Last").Exec(ctx, txExecutor); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}
	// SQL: INSERT INTO users (id, email) VALUES (?, ?) (arguments: 2, "grace@example.com")
	if _, err := store.Users().Create().ID(2).Email("grace@example.com").FirstName("First").LastName("Last").Exec(ctx, txExecutor); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// The table carries every users column as a field, and UsersProjection selects
	// them in the order the generated row type scans them.
	projection, err := store.UsersProjection(users)
	if err != nil {
		fmt.Printf("failed to build users projection: %s\n", err)
		return
	}
	base := rasql.Select(users, projection)

	// The same query shape that runs against db also runs against
	// txExecutor: it reads the two rows written above, before they are committed.
	// SQL: SELECT users.id, users.email, users.nickname, users.status, users.first_name, users.last_name FROM users ORDER BY users.id ASC
	inTx, err := rasql.All(ctx, txExecutor, base.OrderBy(rasql.AscExpr(users.ID.Expr())))
	if err != nil {
		fmt.Printf("failed to query users in transaction: %s\n", err)
		return
	}
	fmt.Printf("%d rows visible in transaction\n", len(inTx))

	if err := tx.Commit(); err != nil {
		fmt.Printf("failed to commit transaction: %s\n", err)
		return
	}

	// Nothing touches db between Begin and Commit above. That is this
	// example's own constraint, not rasql's: SetMaxOpenConns(1) gives it one
	// connection, and the transaction holds it until Commit or Rollback
	// releases it back to the pool.
	// SQL: SELECT users.id, users.email, users.nickname, users.status, users.first_name, users.last_name FROM users ORDER BY users.id ASC
	afterCommit, err := rasql.All(ctx, db, base.OrderBy(rasql.AscExpr(users.ID.Expr())))
	if err != nil {
		fmt.Printf("failed to query users after commit: %s\n", err)
		return
	}
	fmt.Printf("%d rows visible after commit\n", len(afterCommit))

	// Output:
	// 2 rows visible in transaction
	// 2 rows visible after commit
}
