package examples_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_inspect_sqlite_table() {
	// This example reads SQLite tables from main, temp, and an attached database.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	database.SetMaxOpenConns(1)
	defer func() { _ = database.Close() }()
	// Pretend these tables already exist in an application-owned SQLite database.
	if _, err := database.ExecContext(ctx, "ATTACH DATABASE ':memory:' AS aux"); err != nil {
		fmt.Printf("failed to attach aux database: %s\n", err)
		return
	}
	for _, statement := range []string{
		"CREATE TABLE main.users (id INTEGER PRIMARY KEY, main_value TEXT)",
		"CREATE TABLE aux.users (id INTEGER PRIMARY KEY, aux_value TEXT)",
		"CREATE TEMP TABLE users (id INTEGER PRIMARY KEY, temp_value TEXT)",
	} {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			fmt.Printf("failed to create users table: %s\n", err)
			return
		}
	}

	// An unscoped lookup does not guess when several databases contain users.
	// The typed error exposes the conflicting database names to the caller.
	inspector, err := inspect.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create SQLite inspector: %s\n", err)
		return
	}
	_, err = inspector.Table(ctx, "users")
	var ambiguous *inspect.AmbiguousTableError
	if !errors.As(err, &ambiguous) {
		fmt.Printf("expected ambiguous users error, got %v\n", err)
		return
	}
	fmt.Printf("ambiguous %s: %d databases\n", ambiguous.Table, len(ambiguous.Databases))

	for _, databaseName := range []string{"main", "temp", "aux"} {
		table, err := inspector.TableIn(ctx, databaseName, "users")
		if err != nil {
			fmt.Printf("failed to inspect %s.users: %s\n", databaseName, err)
			return
		}
		fmt.Printf("%s.%s: %s\n", table.Schema, table.Name, table.Columns[1].Name)
	}

	// Output:
	// ambiguous users: 3 databases
	// main.users: main_value
	// temp.users: temp_value
	// aux.users: aux_value
}
