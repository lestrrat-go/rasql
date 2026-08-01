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

Importing `runtime` registers the pure-Go SQLite driver as `sqlite`. This runnable example uses an in-memory database, so it creates the table, inserts data, and issues the query in one chain.

<!-- INCLUDE(examples/runtime_sqlite_query_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/row"
	"github.com/lestrrat-go/rasql/runtime"
)

func Example_runtime_sqlite_query() {
	ctx := context.Background()
	// Importing runtime registers the pure-Go SQLite driver as "sqlite".
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	client, err := runtime.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create runtime client: %s\n", err)
		return
	}
	if err := client.CreateTable(ctx, users.Table()); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO users (id, email) VALUES (?, ?)", 42, "ada@example.com"); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// users is a reusable query.TableRef with the shape emitted by rasqlgen.
	rows, err := client.SelectFrom(users).
		Select("id", "email").
		WhereEqual("id", 42).
		Query(ctx)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	if len(rows) != 1 {
		fmt.Printf("expected one user, got %d\n", len(rows))
		return
	}
	email, err := row.String("email")
	if err != nil {
		fmt.Printf("failed to create email column: %s\n", err)
		return
	}
	userEmail, err := email.Get(rows[0])
	if err != nil {
		fmt.Printf("failed to read email: %s\n", err)
		return
	}

	fmt.Println(userEmail)

	// Output:
	// ada@example.com
}
```
source: [examples/runtime_sqlite_query_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/runtime_sqlite_query_example_test.go)
<!-- END INCLUDE -->

# Debug a query

`runtime.Client` also accepts a `runtime.Queryer`, so a debug implementation can print generated SQL without connecting to a database.

<!-- INCLUDE(examples/runtime_debug_query_example_test.go) -->
```go
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
	// runtime.New accepts *sql.DB, *sql.Tx, or another runtime.Queryer. This
	// debug Queryer lets the example show the generated statement without a database.
	client, err := runtime.New(statementPrinter{}, dialect.PostgreSQL())
	if err != nil {
		fmt.Printf("failed to create runtime client: %s\n", err)
		return
	}

	// users is a reusable query.TableRef with the shape emitted by rasqlgen.
	rows, err := client.SelectFrom(users).
		Select("id", "email").
		WhereEqual("id", 42).
		Query(context.Background())
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
```
source: [examples/runtime_debug_query_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/runtime_debug_query_example_test.go)
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
