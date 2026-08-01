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

# Generate from PostgreSQL

Generate reusable table references directly from a live PostgreSQL database.

```sh
rasqlgen schema \
  -dsn "$DATABASE_URL" \
  -table users \
  -package store \
  -output internal/store/rasql_gen.go
```

Repeat `-table` for each table. Generated code exports `store.Users` as a reusable `query.TableRef`; call `store.Users.Table()` when a schema descriptor is required.

# Query a generated table

Create `runtime.Client` once from the application's `*sql.DB`, then issue a query in one chain. The output comment shows the result returned for the selected user.

<!-- INCLUDE(examples/runtime_query_example_test.go) -->
```go
package examples_test

import (
	"context"
	"fmt"

	"github.com/lestrrat-go/rasql/row"
	"github.com/lestrrat-go/rasql/runtime"
)

func queryGeneratedUser(ctx context.Context, client runtime.Client) {
	// Create client once at application startup with
	// runtime.New(db, dialect.PostgreSQL()). Users is the reusable query.TableRef
	// emitted by rasqlgen.
	rows, err := client.SelectFrom(users).
		Select("id", "email").
		WhereEqual("id", 42).
		Query(ctx)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	if len(rows) == 0 {
		fmt.Println("no user found")
		return
	}
	id, err := row.Int64("id")
	if err != nil {
		fmt.Printf("failed to create id column: %s\n", err)
		return
	}
	email, err := row.String("email")
	if err != nil {
		fmt.Printf("failed to create email column: %s\n", err)
		return
	}
	userID, err := id.Get(rows[0])
	if err != nil {
		fmt.Printf("failed to read id: %s\n", err)
		return
	}
	userEmail, err := email.Get(rows[0])
	if err != nil {
		fmt.Printf("failed to read email: %s\n", err)
		return
	}

	fmt.Printf("%d %s\n", userID, userEmail)

	// OUTPUT:
	// 42 ada@example.com
}
```
source: [examples/runtime_query_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/runtime_query_example_test.go)
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

`rasqlgen schema` reads PostgreSQL metadata with `-dsn` and `-table`, or accepts a JSON schema snapshot with `-input`. It generates reusable `query.TableRef` values. `rasqlgen query` generates a parameterized Go function from a restricted SQL template. Both commands reject unchecked template actions and preserve values as bound arguments.

Runnable documentation examples live in [`examples/`](examples/) as executable Go examples.

See [DESIGN.md](DESIGN.md) for the architecture and focused implementation history.
