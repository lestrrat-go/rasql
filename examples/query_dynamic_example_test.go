package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/render"
)

func Example_query_dynamic() {
	// users is a reusable query.TableRef with the shape emitted by rasqlgen.
	// SelectFrom starts a fluent builder. Build returns the parameterized SQL
	// statement that runtime.Client can execute.
	statement, err := render.SelectFrom(dialect.PostgreSQL(), users).
		Select("id", "email").
		WhereEqual("id", 42).
		Build()
	if err != nil {
		fmt.Printf("failed to build select: %s\n", err)
		return
	}

	fmt.Println(statement.SQL())
	fmt.Println(statement.Args())

	// Output:
	// SELECT "users"."id", "users"."email" FROM "users" WHERE ("users"."id" = $1)
	// [42]
}
