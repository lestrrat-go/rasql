# rasql

`rasql` (pronounced “rascal”) is an all-in-one SQL tool for Go.

It provides:

* PostgreSQL, MySQL, SQLite, and Google Cloud Spanner dialects.
* Schema definitions written as Go code, including generation from live database metadata.
* Type-safe result-set access.
* Dynamic query building at runtime.
* Static query building with templates.

# Define a table

<!-- INCLUDE(examples/schema_table_definition_example_test.go) -->
```go
package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/schema"
)

func Example_schema_table_definition() {
	// Describe each database table once with schema.Table. The same descriptor
	// can later supply a reusable query.TableRef or generate DDL.
	table := schema.Table{
		// Name is the database table identifier.
		Name: "users",
		// Columns list each database column and its dialect-neutral logical type.
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		// PrimaryKey names columns from Columns that uniquely identify each row.
		PrimaryKey: []string{"id"},
	}
	if err := table.Validate(); err != nil {
		fmt.Printf("failed to define table: %s\n", err)
		return
	}

	fmt.Printf("%s: %d columns\n", table.Name, len(table.Columns))

	// Output:
	// users: 2 columns
}
```
source: [examples/schema_table_definition_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/schema_table_definition_example_test.go)
<!-- END INCLUDE -->

# SYNOPSIS

<!-- INCLUDE(examples/query_dynamic_example_test.go) -->
```go
package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
)

func Example_query_dynamic() {
	// users is a pre-built table reference reused by each users query.
	// Column references are tied to this table reference. They can be used for
	// projections, predicates, joins, and ordering without manually qualifying
	// their SQL identifiers.
	id, err := users.Column("id")
	if err != nil {
		fmt.Printf("failed to select id column: %s\n", err)
		return
	}
	email, err := users.Column("email")
	if err != nil {
		fmt.Printf("failed to select email column: %s\n", err)
		return
	}
	// NewSelect returns an immutable statement. Each With method returns a new
	// validated statement, so the original can be safely reused by another path.
	statement, err := query.NewSelect(users, query.Project(id), query.Project(email))
	if err != nil {
		fmt.Printf("failed to build select: %s\n", err)
		return
	}
	// Bind keeps the value separate from SQL text. The renderer assigns the
	// placeholder syntax required by the selected database dialect.
	statement, err = statement.WithWhere(query.Equal(id, query.Bind(42)))
	if err != nil {
		fmt.Printf("failed to add predicate: %s\n", err)
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
```
source: [examples/query_dynamic_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/query_dynamic_example_test.go)
<!-- END INCLUDE -->

# Static templates

<!-- INCLUDE(examples/query_static_example_test.go) -->
```go
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
```
source: [examples/query_static_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/query_static_example_test.go)
<!-- END INCLUDE -->

The project requires Go 1.26 or newer and uses parameterized types where they improve type safety or avoid conversions.

The `schema`, `query`, `render`, `row`, and `runtime` packages cover the main application path. The `inspect` package normalizes live table columns and primary keys. The `generate` and `template` packages produce deterministic Go source.

`rasqlgen schema` generates Go table descriptors from a JSON schema snapshot. `rasqlgen query` generates a parameterized Go function from a restricted SQL template. Both commands reject unchecked template actions and preserve values as bound arguments.

Runnable documentation examples live in [`examples/`](examples/) as executable Go examples.

See [DESIGN.md](DESIGN.md) for the architecture and focused implementation history.
