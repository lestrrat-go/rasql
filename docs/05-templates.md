# Static templates

A static template is SQL text written by hand, with named placeholders for the values. Use it when a query is fixed and reads better as SQL than as builder calls, or when it uses syntax the `query` package does not model.

The template language is deliberately tiny. Text is copied through as SQL, and the only action allowed is `{{bind "name"}}`. There is no way to write a template action that becomes SQL text, so a template cannot interpolate a value into the statement even by mistake.

## Compile and bind

<!-- INCLUDE(examples/query_static_example_test.go) -->
```go
package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	querytemplate "github.com/lestrrat-go/rasql/template"
)

func Example_query_static() {
	// This example compiles a named static query and binds one value to it.
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
	// parameterized statement that rasql.Client can execute directly.
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
```
source: [examples/query_static_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/query_static_example_test.go)
<!-- END INCLUDE -->

The three steps are separate because each one has a different lifetime.

1. `Parse` validates the text once, at startup or at generation time. It rejects any action other than a bind. The name is used in error messages.
2. `Compile` turns each named bind into the dialect's placeholder syntax, so `{{bind "email"}}` becomes `$1` for PostgreSQL and `?` for MySQL or SQLite. A parsed template can be compiled for several dialects.
3. `Bind` supplies the values. It requires each name exactly once, and returns a `render.Statement` holding the SQL and its arguments in order.

`Compiled.SQL()` returns the placeholder SQL for logging, and `Compiled.ParameterNames()` lists the names in first-use order, which is useful for checking a template against the values an application intends to pass.

## Execute a bound statement

A bound statement is the same `render.Statement` the builders produce, so the same client runs it.

<!-- INCLUDE(examples/rasql_static_template_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/row"
	querytemplate "github.com/lestrrat-go/rasql/template"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_static_template() {
	// This example binds a static template and executes it through rasql.Client.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer database.Close()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A Client couples a database handle with the dialect used to render SQL.
	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}
	// Create the table described by the generated users descriptor.
	if err := rasql.Create(ctx, client, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	// Insert a row that the bound template will find.
	if _, err := rasql.Insert(ctx, client, users, UserRow{ID: 42, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// Parse accepts only SQL text and named bind actions.
	parsed, err := querytemplate.Parse("user_by_email", "SELECT id, email FROM users WHERE email = {{bind \"email\"}}")
	if err != nil {
		fmt.Printf("failed to parse template: %s\n", err)
		return
	}
	// Compile converts named binds into the selected dialect's placeholders.
	compiled, err := parsed.Compile(dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to compile template: %s\n", err)
		return
	}
	// Bind supplies values without putting them into the SQL text.
	statement, err := compiled.Bind(map[string]any{"email": "ada@example.com"})
	if err != nil {
		fmt.Printf("failed to bind template: %s\n", err)
		return
	}

	// QueryRendered creates the rangeable sequence from the template statement.
	rows, err := client.QueryRendered(ctx, statement)
	if err != nil {
		fmt.Printf("failed to query user: %s\n", err)
		return
	}
	for result, err := range rows {
		if err != nil {
			fmt.Printf("failed to query user: %s\n", err)
			return
		}
		email, err := row.Get[string](result, "email")
		if err != nil {
			fmt.Printf("failed to read email: %s\n", err)
			return
		}
		fmt.Println(email)
	}

	// Output:
	// ada@example.com
}
```
source: [examples/rasql_static_template_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_static_template_example_test.go)
<!-- END INCLUDE -->

`QueryRendered` returns rows, and `ExecRendered` runs a statement that returns none. Because the result shape comes from hand-written SQL, rows arrive as `row.Row` rather than a generated type. Read them in one of three ways:

| Call | Gives |
| --- | --- |
| `row.Get[T](result, "email")` | One named value, decoded as `T`. |
| `row.Assign(result, "email", &value)` | The same value, decoded into an existing destination. |
| `row.Decode[T](result)` | A whole struct, through its `DecodeRow` method when it has one, and matching `rasql` tags or snake-cased field names otherwise. |
| `row.String("email")` and friends | A reusable typed column, decoded with `Column.Get(result)`. |

## Generate a function instead

`Compiled.GoSource` emits a Go function that builds the statement, so a template can be compiled at build time rather than at startup. `rasqlgen query` is the command-line front end for it; see [`rasqlgen`](06-rasqlgen.md).

## Next

[`rasqlgen`](06-rasqlgen.md) generates table descriptors and query functions as Go source.
