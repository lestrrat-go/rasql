# Static templates

A static template is SQL text written by hand, with named placeholders for the values. Use it when a query is fixed and reads better as SQL than as builder calls, or when it uses syntax the `query` package does not model.

Compiling and binding a template belongs to the same layer as [the SQL builder](04-sql-builder.md): both end at a `render.Statement`, and neither needs a generated table or a Go row type. The examples below create and seed their fixture rows with the typed helpers because that is the shortest setup, and `rasql.QueryRenderedAll[T]` decodes a result into a Go type when the selected names line up with its fields.

The template language is deliberately tiny. Text is copied through as SQL, and the only action allowed is `{{bind "name"}}`. The `{{` delimiter is reserved, so SQL text and comments cannot contain that literal sequence. There is no way to write a template action that becomes SQL text, so a template cannot interpolate a value into the statement even by mistake.

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
	// parameterized statement that rasql.DB can execute directly.
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

A bound statement is the same `render.Statement` the builders produce, so the same `DB` runs it.

<!-- INCLUDE(examples/rasql_static_template_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/dynamic"
	querytemplate "github.com/lestrrat-go/rasql/template"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_static_template() {
	// This example binds a static template and executes it through rasql.DB.
	// users and UserRow are declared in query_example_tables_test.go with the
	// shape rasqlgen emits; an application that generated into package store
	// would write store.Users() and store.UsersRow instead.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A DB couples a database handle with the dialect used to render SQL.
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	// Create the table described by the generated users descriptor.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	// Insert a row that the bound template will find.
	if _, err := rasql.Insert(ctx, db, users, UserRow{ID: 42, Email: "ada@example.com"}); err != nil {
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

	// SQL: SELECT id, email FROM users WHERE email = ? (argument: "ada@example.com")
	// QueryRendered runs the template statement; dynamic.Scan turns its rows into a rangeable sequence.
	sqlRows, err := db.QueryRendered(ctx, statement)
	if err != nil {
		fmt.Printf("failed to query user: %s\n", err)
		return
	}
	for result, err := range dynamic.Scan(sqlRows) {
		if err != nil {
			fmt.Printf("failed to query user: %s\n", err)
			return
		}
		email, err := dynamic.Get[string](result, "email")
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

`QueryRendered` returns rows, and `ExecRendered` runs a statement that returns none. Because the result shape comes from hand-written SQL, rows arrive as `dynamic.Row` by default. Use `rasql.QueryRendered[T]`, `rasql.QueryRenderedAll[T]`, or `rasql.QueryRenderedOne[T]` when the selected column names map to a Go result type. The typed helpers use the same row decoding as typed builders: `rasql` tags, and snake-cased field names for untagged fields.

<!-- INCLUDE(examples/rasql_typed_static_template_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	querytemplate "github.com/lestrrat-go/rasql/template"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

type rankedUser struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
	Rank  int64  `rasql:"rank"`
}

func Example_rasql_typed_static_template() {
	// A static template keeps complex SQL readable while QueryRenderedAll maps
	// its result into a normal Go type.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)

	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	for _, user := range []UserRow{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@example.com"},
	} {
		if _, err := rasql.Insert(ctx, db, users, user); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	parsed, err := querytemplate.Parse("ranked_users", `WITH ranked_users AS (
		SELECT id, email, ROW_NUMBER() OVER (ORDER BY id) AS rank
		FROM users
	)
	SELECT id, email, rank FROM ranked_users WHERE id >= {{bind "minimum_id"}} ORDER BY rank`)
	if err != nil {
		fmt.Printf("failed to parse template: %s\n", err)
		return
	}
	compiled, err := parsed.Compile(dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to compile template: %s\n", err)
		return
	}
	statement, err := compiled.Bind(map[string]any{"minimum_id": 2})
	if err != nil {
		fmt.Printf("failed to bind template: %s\n", err)
		return
	}
	// SQL: WITH ranked_users AS (SELECT id, email, ROW_NUMBER() OVER (ORDER BY id) AS rank FROM users) SELECT id, email, rank FROM ranked_users WHERE id >= ? ORDER BY rank (argument: 2)
	rows, err := rasql.QueryRenderedAll[rankedUser](ctx, db, statement)
	if err != nil {
		fmt.Printf("failed to query ranked users: %s\n", err)
		return
	}
	for _, user := range rows {
		fmt.Println(user.Rank, user.Email)
	}

	// Output:
	// 2 bob@example.com
	// 3 cyd@example.com
}
```
source: [examples/rasql_typed_static_template_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_typed_static_template_example_test.go)
<!-- END INCLUDE -->

The template parser still permits only `{{bind "name"}}` actions, and `Compile` still chooses placeholders from the selected dialect. `QueryRendered` does not parse or validate the SQL grammar, table names, column names, or database-specific features; the target database checks those when it executes the statement. The fluent builder remains the place for dialect-neutral validation of its supported syntax. CTEs, window functions, recursive queries, vendor-specific clauses, and other syntax not modeled by the builder must use a static template or `render.Precompiled` statement.

Read dynamic results in one of three ways:

| Call | Gives |
| --- | --- |
| `dynamic.Get[T](result, "email")` | One named value, decoded as `T`. |
| `dynamic.Assign(result, "email", &value)` | The same value, decoded into an existing destination. |
| `dynamic.Decode[T](result)` | A whole struct, matching `rasql` tags or snake-cased field names. |

## Generate a function instead

`Compiled.GoSource` emits a Go function that builds the statement, so a template can be compiled at build time rather than at startup. Put the template in the `queries` list of `rasql.json`; that keeps table generation and static query generation in one command. See [`rasql codegen`](08-rasqlgen.md#static-query-functions).

## Next

[`rasqlgen`](08-rasqlgen.md) generates table descriptors and query functions as Go source.
