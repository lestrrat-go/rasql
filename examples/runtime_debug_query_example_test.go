package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/runtime"
)

// statementPrinter is a debug-only runtime.Queryer. It follows the same
// QueryContext contract as *sql.DB, but prints statements instead of running them.
type statementPrinter struct{}

func (statementPrinter) QueryContext(_ context.Context, query string, arguments ...any) (*sql.Rows, error) {
	fmt.Println(query)
	fmt.Printf("%v\n", arguments)
	return nil, nil
}

func Example_runtime_debug_query() {
	// This example prints the SQL for a typed query without opening a database.
	// runtime.New accepts *sql.DB, *sql.Tx, or another runtime.Queryer. This
	// debug Queryer lets the example show the generated statement without a database.
	client, err := runtime.New(statementPrinter{}, dialect.PostgreSQL())
	if err != nil {
		fmt.Printf("failed to create runtime client: %s\n", err)
		return
	}

	// users is a typed table descriptor with the shape emitted by rasqlgen.
	rows, err := runtime.SelectFrom(client, users).WhereEqual("id", 42).All(context.Background())
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}

	fmt.Printf("%d result rows\n", len(rows))

	// Output:
	// SELECT "users"."id", "users"."email" FROM "users" WHERE ("users"."id" = $1)
	// [42]
	// 0 result rows
}
