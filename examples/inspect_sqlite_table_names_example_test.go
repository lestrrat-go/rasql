package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_inspect_sqlite_table_names() {
	// This example enumerates the base tables across main and an attached
	// database, including a table name that exists in both.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	connection, err := database.Conn(ctx)
	if err != nil {
		fmt.Printf("failed to retain SQLite connection: %s\n", err)
		return
	}
	defer func() { _ = connection.Close() }()
	if _, err := connection.ExecContext(ctx, "ATTACH DATABASE ':memory:' AS tenant"); err != nil {
		fmt.Printf("failed to attach tenant database: %s\n", err)
		return
	}
	for _, statement := range []string{
		"CREATE TABLE main.armadillos (id INTEGER PRIMARY KEY)",
		"CREATE TABLE main.zebras (id INTEGER PRIMARY KEY)",
		"CREATE TABLE tenant.zebras (id INTEGER PRIMARY KEY)",
		// A view is not a base table, so TableNames excludes it.
		"CREATE VIEW main.zebra_view AS SELECT id FROM main.zebras",
	} {
		if _, err := connection.ExecContext(ctx, statement); err != nil {
			fmt.Printf("failed to run %q: %s\n", statement, err)
			return
		}
	}

	inspector, err := inspect.New(connection, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create SQLite inspector: %s\n", err)
		return
	}
	// TableNames reports every database's tables together; TableRef.Schema
	// is what keeps the two "zebras" tables distinguishable.
	refs, err := inspector.TableNames(ctx)
	if err != nil {
		fmt.Printf("failed to list table names: %s\n", err)
		return
	}
	for _, ref := range refs {
		fmt.Printf("%s.%s\n", ref.Schema, ref.Name)
	}

	// Output:
	// main.armadillos
	// main.zebras
	// tenant.zebras
}
