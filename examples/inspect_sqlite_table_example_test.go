package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	_ "github.com/lestrrat-go/rasql/runtime"
)

func Example_inspect_sqlite_table() {
	ctx := context.Background()
	// The runtime package registers the pure-Go SQLite driver as "sqlite".
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer database.Close()
	if _, err := database.ExecContext(ctx, "CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL, nickname TEXT)"); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	inspector, err := inspect.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create SQLite inspector: %s\n", err)
		return
	}
	table, err := inspector.Table(ctx, "users")
	if err != nil {
		fmt.Printf("failed to inspect users table: %s\n", err)
		return
	}
	fmt.Printf("%s: %s, %s, %s\n", table.Name, table.Columns[0].Type, table.Columns[1].Type, table.Columns[2].Type)
	fmt.Println(table.PrimaryKey)

	// Output:
	// users: integer, text, text
	// [id]
}
