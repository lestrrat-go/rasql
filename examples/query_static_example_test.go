package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	querytemplate "github.com/lestrrat-go/rasql/template"
)

func Example_query_static() {
	// Templates accept SQL text and only {{bind "name"}} actions. Values cannot
	// become SQL text because every action becomes a dialect placeholder.
	parsed, err := querytemplate.Parse("user_by_email", "SELECT id FROM users WHERE email = {{bind \"email\"}}")
	if err != nil {
		fmt.Printf("failed to parse template: %s\n", err)
		return
	}
	// Compile converts the restricted binding actions to this dialect's static
	// placeholder syntax. It can also generate a Go function through rasqlgen.
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	if err != nil {
		fmt.Printf("failed to compile template: %s\n", err)
		return
	}
	// Bind requires each named value exactly once and returns a precompiled,
	// parameterized statement that runtime.Client can execute directly.
	statement, err := compiled.Bind(map[string]any{"email": "ada@example.com"})
	if err != nil {
		fmt.Printf("failed to bind template: %s\n", err)
		return
	}

	fmt.Println(statement.SQL())
	fmt.Println(statement.Args())

	// Output:
	// SELECT id FROM users WHERE email = $1
	// [ada@example.com]
}
