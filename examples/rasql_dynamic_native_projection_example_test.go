package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite"
)

// Example_rasql_dynamicNativeProjection solves the case where handwritten SQL
// returns a result shape known only at runtime. DynamicProjection matches the
// declared result columns to struct tags, so SQL column order does not have to
// match Go field order.
func Example_rasql_dynamicNativeProjection() {
	ctx := context.Background()
	// Keep one SQLite connection because each connection gets a separate
	// in-memory database.
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)
	if _, err := database.ExecContext(ctx, "CREATE TABLE users (id INTEGER, display_name TEXT)"); err != nil {
		fmt.Printf("failed to create table: %s\n", err)
		return
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO users VALUES (7, 'Ada')"); err != nil {
		fmt.Printf("failed to insert row: %s\n", err)
		return
	}

	db, err := rasql.Open(ctx, database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create executor: %s\n", err)
		return
	}
	// Describe columns in SQL order. The query returns display_name before id,
	// even though the Go struct declares ID before Name.
	resultSchema, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "display_name", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
	)
	if err != nil {
		fmt.Printf("failed to define result schema: %s\n", err)
		return
	}
	type user struct {
		ID   int64  `rasql:"id"`
		Name string `rasql:"display_name"`
	}
	// DynamicProjection uses rasql tags to map those runtime columns to fields
	// instead of relying on the struct's declaration order.
	projection, err := rasql.DynamicProjection[user](resultSchema)
	if err != nil {
		fmt.Printf("failed to define dynamic projection: %s\n", err)
		return
	}
	// Native preserves the handwritten statement while attaching the typed
	// projection and the expected many-row cardinality.
	query, err := rasql.Native(rasql.NativeStatement{
		Engine: "sqlite",
		SQL:    "SELECT display_name, id FROM users",
	}, projection, rasql.Many)
	if err != nil {
		fmt.Printf("failed to define native query: %s\n", err)
		return
	}
	rows, err := rasql.All(ctx, db, query)
	if err != nil {
		fmt.Printf("failed to execute query: %s\n", err)
		return
	}
	for _, row := range rows {
		fmt.Println(row.ID, row.Name)
	}
	// Output:
	// 7 Ada
}
