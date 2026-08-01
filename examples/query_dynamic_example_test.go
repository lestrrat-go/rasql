package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
)

func Example_query_dynamic() {
	// SelectFrom starts an immutable fluent builder from a reusable table
	// reference. Select validates column names, and WhereEqual binds its value.
	statement, err := query.SelectFrom(users).
		Select("id", "email").
		WhereEqual("id", 42).
		Build()
	if err != nil {
		fmt.Printf("failed to build select: %s\n", err)
		return
	}
	// The rendered statement contains SQL and arguments ready for runtime.Client
	// or database/sql. The value 42 is never interpolated into SQL text.
	rendered, err := render.Select(dialect.PostgreSQL(), statement)
	if err != nil {
		fmt.Printf("failed to render select: %s\n", err)
		return
	}

	fmt.Println(rendered.SQL())
	fmt.Println(rendered.Args())

	// Output:
	// SELECT "users"."id", "users"."email" FROM "users" WHERE ("users"."id" = $1)
	// [42]
}
