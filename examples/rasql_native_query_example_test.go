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

func Example_rasql_nativeQuery() {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create database: %s\n", err)
		return
	}
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	if err != nil {
		fmt.Printf("failed to create profile: %s\n", err)
		return
	}
	executor, err := rasql.AsExecutor(db, profile)
	if err != nil {
		fmt.Printf("failed to create executor: %s\n", err)
		return
	}
	projection, err := rasql.Scalar("value", rasql.Value(0), schema.IntegerType{}, "")
	if err != nil {
		fmt.Printf("failed to create projection: %s\n", err)
		return
	}
	query, err := rasql.Native(rasql.NativeStatement{Engine: "sqlite", SQL: "SELECT 7 AS value"}, projection, rasql.ExactlyOne)
	if err != nil {
		fmt.Printf("failed to create native query: %s\n", err)
		return
	}
	value, err := rasql.One(context.Background(), executor, query)
	if err != nil {
		fmt.Printf("failed to run native query: %s\n", err)
		return
	}
	fmt.Println(value)
	// Output:
	// 7
}
