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

func Example_execAtomic() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create database: %s\n", err)
		return
	}
	if _, err := db.ExecRendered(ctx, stmt.New("CREATE TABLE items (value INTEGER)")); err != nil {
		fmt.Printf("failed to create table: %s\n", err)
		return
	}
	sentinel := errors.New("nested operation failed")
	err = db.Atomic(ctx, nil, func(ctx context.Context, tx rasql.DB) error {
		if _, err := tx.ExecRendered(ctx, stmt.New("INSERT INTO items VALUES (1)")); err != nil {
			return err
		}
		err := tx.Atomic(ctx, nil, func(ctx context.Context, nested rasql.DB) error {
			if _, err := nested.ExecRendered(ctx, stmt.New("INSERT INTO items VALUES (2)")); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			return err
		}
		_, err = tx.ExecRendered(ctx, stmt.New("INSERT INTO items VALUES (3)"))
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
