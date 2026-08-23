# The database handle

A `rasql.DB` pairs a database handle with the dialect used to render SQL. Both builders in [Querying](../02-querying.md) end here when they execute, and a statement rendered by hand runs through the same value.

`rasql.New(handle, dialect, hooks…)` builds one. It opens no connection and starts no transaction, and it accepts anything satisfying `rasql.Handle`, which `*sql.DB` and `*sql.Tx` both do. A custom `Handle` prints or records statements in place of running them, which [Typed queries](../orm/03-typed-queries.md#see-the-sql-without-a-database) shows.

## Run a rendered statement

[The SQL builder](02-sql-builder.md) ends at a `statement.Statement`, which holds SQL text and its arguments. `database/sql` runs one directly. Running it through a `rasql.DB` instead applies the registered hooks and decodes the rows:

| Call | Runs |
| --- | --- |
| `db.QueryRendered(ctx, statement)` | A rendered statement, returning the `*sql.Rows` the caller owns; [`dynamic.Scan`](05-dynamic.md#read-a-row) turns those into `dynamic.Row` values and closes them. |
| `db.ExecRendered(ctx, statement)` | A rendered statement that returns no rows. |
| `rasql.QueryRenderedAll[T](ctx, db, statement)` | The same, decoded into `T`. |
| `rasql.Exec(ctx, db, statement)` | A `query.WriteStatement`, rendering it on the way. It rejects a write carrying a `RETURNING` clause, which `dynamic.QueryWrite` or `rasql.QueryWriteAll[T]` reads instead. |
| `dynamic.Query(ctx, db, statement)` | A `query.Select`, rendering it on the way. |

`rasql.Exec` and `dynamic.Query` take the statement rather than the rendered text, because a `rasql.DB` already holds the dialect to render with. `rasql.Exec` rejects a write carrying a `RETURNING` clause, and `dynamic.QueryWrite` reads the rows of one instead. [Writing rows](03-write-statements.md) covers the write side of that path, and [Dynamic rows](05-dynamic.md) covers the `dynamic` calls named here.

## Operational hooks

`rasql.Hook` provides a small observation and policy boundary around rendered database operations. A hook receives the operation kind, the exact SQL text, and a copy of the bound arguments. A `Before` hook can reject a statement before it reaches `database/sql`. An `After` hook receives the execution error, if any, and can report or reject the result.

Hooks run in registration order before execution and reverse registration order after execution. A hook cannot replace the SQL or its bound arguments, so policy checks and logging do not change the statement sent to the driver.

<!-- INCLUDE(examples/rasql_hook_example_test.go#hook) -->
```go
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
```
source: [examples/rasql_hook_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_hook_example_test.go)
<!-- END INCLUDE -->

Pass hooks to `rasql.New`, or add them with `DB.WithHooks`. A transaction started by `DB.Begin` inherits every hook already registered on that `DB`, then appends any hooks passed to `Begin` itself. A policy hook registered on a `DB` therefore also runs for operations inside transactions started from it, not just for calls made directly through the `DB`. Add hooks scoped to only the transaction by passing them to `Begin`, or by calling `WithHooks` on the `DB` it returns. `WithHooks` returns the same concrete `DB` value, so transaction ownership and explicit `Commit` or `Rollback` remain visible.

Hooks cover calls through `DB`, including transactions, the high-level builders, and static rendered statements. They do not wrap `Begin`, `Commit`, `Rollback`, direct `database/sql` calls, or the migration and inspection packages, which use their own database handles. Hooks are synchronous, and they add no retries, tracing spans, tenant filters, or automatic redaction. An application implements those policies in its own hooks or at its database boundary.

## Transactions

A transaction is not a separate type. `DB.Begin` takes `*sql.TxOptions` and optional hooks, which may be omitted, starts a transaction on the same handle and dialect the `DB` was built from, and returns another `rasql.DB` bound to it. That returned `DB` is a transaction, so every builder terminal and every free function that takes a `rasql.DB` — `rasql.Insert`, `rasql.Update`, `rasql.CreateTable`, and the rest — accepts it exactly as it accepts `db`, because both values share the one `rasql.DB` type.

The caller owns the transaction. `defer tx.Rollback()` immediately after `Begin` is the intended shape, because `Rollback` reports nothing once the transaction is finished, whether by a successful `Commit`, an earlier `Rollback`, or a context cancellation. Calling `Commit` or `Rollback` on a `DB` that is not a transaction returns an error rather than being a compile-time mistake, since one concrete type now covers both cases.

A transaction still cannot be nested: calling `Begin` on a `DB` that is already a transaction returns an error instead of opening a savepoint. An application that already holds a native `*sql.Tx` can hand it straight to `rasql.New` instead of calling `Begin`. The resulting `DB` is a transaction the same way one returned by `Begin` is.

<!-- INCLUDE(examples/rasql_transaction_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_transaction() {
	// This example writes two rows and reads them back inside one transaction,
	// then reads them again through the plain db after it commits.
	// users and UserRow are declared in query_example_tables_test.go with the
	// shape rasqlgen emits; an application that generated into package store
	// would write store.Users() and store.UsersRow instead.
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
	// Create the table before any transaction starts.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	// db.Begin starts a transaction on the same handle and returns another DB
	// bound to it. There is no separate transaction type to carry around: tx is
	// a DB, so everything below takes it exactly as it takes db.
	tx, err := db.Begin(ctx, nil)
	if err != nil {
		fmt.Printf("failed to begin transaction: %s\n", err)
		return
	}
	// Rollback reports nothing once Commit has already succeeded, which is what
	// makes this bare defer correct rather than an error every caller discards.
	defer func() { _ = tx.Rollback() }()

	// SQL: INSERT INTO users (id, email) VALUES (?, ?) (arguments: 1, "ada@example.com")
	if _, err := rasql.Insert(ctx, tx, users, UserRow{ID: 1, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}
	// SQL: INSERT INTO users (id, email) VALUES (?, ?) (arguments: 2, "grace@example.com")
	if _, err := rasql.Insert(ctx, tx, users, UserRow{ID: 2, Email: "grace@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// The same builder shape that runs against db also runs against tx: it
	// reads the two rows written above, before they are committed.
	// SQL: SELECT users.id, users.email FROM users ORDER BY users.id ASC
	inTx, err := rasql.SelectFrom(users).OrderAsc(users.ID()).All(ctx, tx)
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
	// SQL: SELECT users.id, users.email FROM users ORDER BY users.id ASC
	afterCommit, err := rasql.SelectFrom(users).OrderAsc(users.ID()).All(ctx, db)
	if err != nil {
		fmt.Printf("failed to query users after commit: %s\n", err)
		return
	}
	fmt.Printf("%d rows visible after commit\n", len(afterCommit))

	// Output:
	// 2 rows visible in transaction
	// 2 rows visible after commit
}
```
source: [examples/rasql_transaction_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_transaction_example_test.go)
<!-- END INCLUDE -->


## Next

[Named SQL](06-named-sql.md) covers fixed SQL text with named binds, which this handle runs like any other rendered statement.
