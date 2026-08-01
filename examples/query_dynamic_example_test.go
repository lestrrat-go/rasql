package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/render"
)

func Example_query_dynamic() {
	// SelectFrom starts an immutable fluent builder. Select validates column
	// names, WhereEqual binds its value, and Build returns parameterized SQL.
	rendered, err := render.SelectFrom(dialect.PostgreSQL(), users).
		Select("id", "email").
		WhereEqual("id", 42).
		Build()
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
