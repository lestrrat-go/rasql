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

func Example_rasql_dynamicNativeProjection() {
	ctx := context.Background()
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

	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create database: %s\n", err)
		return
	}
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	if err != nil {
		fmt.Printf("failed to create engine profile: %s\n", err)
		return
	}
	executor, err := rasql.AsExecutor(db, profile)
	if err != nil {
		fmt.Printf("failed to create executor: %s\n", err)
		return
	}
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
	projection, err := rasql.DynamicProjection[user](resultSchema)
	if err != nil {
		fmt.Printf("failed to define dynamic projection: %s\n", err)
		return
	}
	query, err := rasql.Native(rasql.NativeStatement{
		Engine: "sqlite",
		SQL:    "SELECT display_name, id FROM users",
	}, projection, rasql.Many)
	if err != nil {
		fmt.Printf("failed to define native query: %s\n", err)
		return
	}
	rows, err := rasql.All(ctx, executor, query)
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
