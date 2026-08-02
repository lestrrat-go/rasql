package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/sample/taskboard/internal/store"
	"github.com/lestrrat-go/rasql/sample/taskboard/internal/taskboard"
	_ "modernc.org/sqlite"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "taskboard: %s\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, output io.Writer) error {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return fmt.Errorf("open SQLite database: %w", err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	// SQLite enables foreign keys per connection, so keep its configuration and
	// the in-memory database on this single connection.
	if _, err := database.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("enable SQLite foreign keys: %w", err)
	}
	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		return fmt.Errorf("create rasql client: %w", err)
	}
	if err := store.CreateSchema(ctx, client); err != nil {
		return fmt.Errorf("create taskboard schema: %w", err)
	}
	return taskboard.Run(ctx, store.New(client), output)
}
