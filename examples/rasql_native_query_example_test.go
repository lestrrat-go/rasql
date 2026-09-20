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

// Example_rasql_nativeQuery solves the case where the query builder cannot
// express an engine-specific statement. Native keeps the SQL text while a
// scalar projection and cardinality preserve typed decoding through rasql.One.
func Example_rasql_nativeQuery() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// Open provides the engine profile that checks the native statement's
	// declared engine before execution.
	db, err := rasql.Open(ctx, database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create executor: %s\n", err)
		return
	}
	// The expression is only a typed description of the returned column here;
	// Native uses the handwritten SELECT rather than rendering this value.
	projection, err := rasql.Scalar("value", rasql.Value(0), schema.IntegerType{}, "")
	if err != nil {
		fmt.Printf("failed to create projection: %s\n", err)
		return
	}
	// ExactlyOne lets One reject both an empty result and an unexpected second
	// row instead of silently accepting either shape.
	query, err := rasql.Native(rasql.NativeStatement{Engine: "sqlite", SQL: "SELECT 7 AS value"}, projection, rasql.ExactlyOne)
	if err != nil {
		fmt.Printf("failed to create native query: %s\n", err)
		return
	}
	value, err := rasql.One(ctx, db, query)
	if err != nil {
		fmt.Printf("failed to run native query: %s\n", err)
		return
	}
	fmt.Println(value)
	// Output:
	// 7
}
