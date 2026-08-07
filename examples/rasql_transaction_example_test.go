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
	// then reads them again through the plain client after it commits.
	// users and UserRow are declared in query_example_tables_test.go with the
	// shape rasqlgen emits; an application that generated into package store
	// would write store.Users() and store.UsersRow instead.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer database.Close()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A Client couples a database handle with the dialect used to render SQL.
	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}
	// Create the table before any transaction starts.
	if err := rasql.Create(ctx, client, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	// Begin takes the *sql.DB, not the client: a Tx renders its own statements
	// and does not go through client at all.
	tx, err := rasql.Begin(ctx, database, dialect.SQLite(), nil)
	if err != nil {
		fmt.Printf("failed to begin transaction: %s\n", err)
		return
	}
	// Rollback reports nothing once Commit has already succeeded, which is what
	// makes this bare defer correct rather than an error every caller discards.
	defer tx.Rollback()

	if _, err := rasql.Insert(ctx, tx, users, UserRow{ID: 1, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}
	if _, err := rasql.Insert(ctx, tx, users, UserRow{ID: 2, Email: "grace@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// The same builder shape that runs against client also runs against tx: it
	// reads the two rows written above, before they are committed.
	inTx, err := rasql.SelectFrom(users).OrderAsc(users.ID).All(ctx, tx)
	if err != nil {
		fmt.Printf("failed to query users in transaction: %s\n", err)
		return
	}
	fmt.Printf("%d rows visible in transaction\n", len(inTx))

	if err := tx.Commit(); err != nil {
		fmt.Printf("failed to commit transaction: %s\n", err)
		return
	}

	// Nothing touches client between Begin and Commit above. That is this
	// example's own constraint, not rasql's: SetMaxOpenConns(1) gives it one
	// connection, and the transaction holds it until Commit or Rollback
	// releases it back to the pool.
	afterCommit, err := rasql.SelectFrom(users).OrderAsc(users.ID).All(ctx, client)
	if err != nil {
		fmt.Printf("failed to query users after commit: %s\n", err)
		return
	}
	fmt.Printf("%d rows visible after commit\n", len(afterCommit))

	// Output:
	// 2 rows visible in transaction
	// 2 rows visible after commit
}
