package examples_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_inspect_sqlite_table_names() {
	// This example enumerates the base tables in a SQLite database.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	for _, statement := range []string{
		"CREATE TABLE zebras (id INTEGER PRIMARY KEY)",
		"CREATE TABLE armadillos (id INTEGER PRIMARY KEY)",
		// A view is not a base table, so TableNames excludes it.
		"CREATE VIEW zebra_view AS SELECT id FROM zebras",
	} {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			fmt.Printf("failed to run %q: %s\n", statement, err)
			return
		}
	}

	inspector, err := inspect.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create SQLite inspector: %s\n", err)
		return
	}
	names, err := inspector.TableNames(ctx)
	if err != nil {
		fmt.Printf("failed to list table names: %s\n", err)
		return
	}
	fmt.Printf("tables: %s\n", strings.Join(names, ", "))

	// Output:
	// tables: armadillos, zebras
}
