package examples_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/stmt"
	_ "modernc.org/sqlite"
)

// Example_execAtomic solves the case where one part of a larger transaction
// may fail without cancelling the work around it. An outer Atomic call starts
// the transaction, while the nested call uses a savepoint that rolls back only
// its own insert.
func Example_execAtomic() {
	ctx := context.Background()
	// Keep one connection so every operation reaches the same in-memory
	// SQLite database and the same transaction.
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)
	db, err := rasql.Open(ctx, database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create database: %s\n", err)
		return
	}
	if _, err := db.Exec(ctx, stmt.New("CREATE TABLE items (value INTEGER)")); err != nil {
		fmt.Printf("failed to create table: %s\n", err)
		return
	}
	// Returning this sentinel from the nested callback makes that savepoint
	// roll back without making the failure ambiguous to the outer callback.
	sentinel := errors.New("nested operation failed")
	// Atomic commits when its callback returns nil and rolls back when the
	// callback returns an error.
	err = db.Atomic(ctx, nil, func(ctx context.Context, tx rasql.DB) error {
		if _, err := tx.Exec(ctx, stmt.New("INSERT INTO items VALUES (1)")); err != nil {
			return err
		}
		// Calling Atomic on the transactional DB creates a savepoint. The row
		// with value 2 belongs to that savepoint and disappears with it.
		err := tx.Atomic(ctx, nil, func(ctx context.Context, nested rasql.DB) error {
			if _, err := nested.Exec(ctx, stmt.New("INSERT INTO items VALUES (2)")); err != nil {
				return err
			}
			return sentinel
		})
		// Consume the expected nested failure so the outer transaction can
		// continue and commit values 1 and 3.
		if !errors.Is(err, sentinel) {
			return err
		}
		_, err = tx.Exec(ctx, stmt.New("INSERT INTO items VALUES (3)"))
		return err
	})
	if err != nil {
		fmt.Printf("atomic operation failed: %s\n", err)
		return
	}
	var values string
	if err := database.QueryRowContext(ctx, "SELECT group_concat(value, ',') FROM items").Scan(&values); err != nil {
		fmt.Printf("failed to read rows: %s\n", err)
		return
	}
	fmt.Println(values)

	// Output:
	// 1,3
}
