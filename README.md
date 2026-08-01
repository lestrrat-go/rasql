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

Repeat `-table` for each table. Generated code exports `store.Users` as a typed `runtime.Table[store.UsersRow]`; call `store.Users.Ref()` for its reusable `query.TableRef`.

# Inspect a SQLite table

Inspect an existing SQLite table when you need its normalized `schema.Table` definition.

<!-- INCLUDE(examples/inspect_sqlite_table_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	_ "github.com/lestrrat-go/rasql/runtime"
)

func Example_inspect_sqlite_table() {
	ctx := context.Background()
	// The runtime package registers the pure-Go SQLite driver as "sqlite".
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer database.Close()
	if _, err := database.ExecContext(ctx, "CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL, nickname TEXT)"); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	inspector, err := inspect.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create SQLite inspector: %s\n", err)
		return
	}
	table, err := inspector.Table(ctx, "users")
	if err != nil {
		fmt.Printf("failed to inspect users table: %s\n", err)
		return
	}
	fmt.Printf("%s: %s, %s, %s\n", table.Name, table.Columns[0].Type, table.Columns[1].Type, table.Columns[2].Type)
	fmt.Println(table.PrimaryKey)

	// Output:
	// users: integer, text, text
	// [id]
}
```
source: [examples/inspect_sqlite_table_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/inspect_sqlite_table_example_test.go)
<!-- END INCLUDE -->

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
	if err := client.CreateTable(ctx, users.Ref().Table()); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if _, err := runtime.Insert(ctx, client, users, UserRow{ID: 42, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// users is a typed table descriptor with the shape emitted by rasqlgen.
	user, err := runtime.SelectFrom(client, users).WhereEqual("id", 42).One(ctx)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}

	fmt.Println(user.Email)

	// Output:
	// ada@example.com
}
```
source: [examples/runtime_sqlite_query_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/runtime_sqlite_query_example_test.go)
<!-- END INCLUDE -->

# Query multiple typed rows

Use `All` when the result has the generated row type, including when ordering, paging, or limiting the result.

<!-- INCLUDE(examples/runtime_typed_query_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/runtime"
)

func Example_runtime_typed_query() {
	ctx := context.Background()
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
	if err := client.CreateTable(ctx, users.Ref().Table()); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	for _, user := range []UserRow{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@example.com"},
	} {
		if _, err := runtime.Insert(ctx, client, users, user); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// SelectFrom knows the UsersRow result type from users. It selects every
	// column, then All decodes every matching row into that type.
	found, err := runtime.SelectFrom(client, users).
		OrderAsc("email").
		Offset(1).
		Limit(2).
		All(ctx)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	for _, user := range found {
		fmt.Println(user.Email)
	}

	// Output:
	// bob@example.com
	// cyd@example.com
}
```
source: [examples/runtime_typed_query_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/runtime_typed_query_example_test.go)
<!-- END INCLUDE -->

# Build a dynamic projection

Use the raw builder for joins and result shapes that do not map to one generated row type. Read its dynamic rows with `row.Get[T]`.

<!-- INCLUDE(examples/runtime_dynamic_projection_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/row"
	"github.com/lestrrat-go/rasql/runtime"
	"github.com/lestrrat-go/rasql/schema"
)

func Example_runtime_dynamic_projection() {
	ctx := context.Background()
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
	type orderRow struct {
		ID     int `rasql:"id"`
		UserID int `rasql:"user_id"`
		Total  int `rasql:"total"`
	}
	orders := runtime.MustTable[orderRow](query.MustNewTableRef(schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
			{Name: "total", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	}))
	if err := client.CreateTable(ctx, users.Ref().Table()); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if err := client.CreateTable(ctx, orders.Ref().Table()); err != nil {
		fmt.Printf("failed to create orders table: %s\n", err)
		return
	}

	userID, err := users.Ref().Column("id")
	if err != nil {
		fmt.Printf("failed to find users.id: %s\n", err)
		return
	}
	email, err := users.Ref().Column("email")
	if err != nil {
		fmt.Printf("failed to find users.email: %s\n", err)
		return
	}
	orderUserID, err := orders.Ref().Column("user_id")
	if err != nil {
		fmt.Printf("failed to find orders.user_id: %s\n", err)
		return
	}
	total, err := orders.Ref().Column("total")
	if err != nil {
		fmt.Printf("failed to find orders.total: %s\n", err)
		return
	}
	if _, err := runtime.Insert(ctx, client, users, UserRow{ID: 1, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}
	for _, order := range []orderRow{
		{ID: 1, UserID: 1, Total: 50},
		{ID: 2, UserID: 1, Total: 10},
	} {
		if _, err := runtime.Insert(ctx, client, orders, order); err != nil {
			fmt.Printf("failed to insert order: %s\n", err)
			return
		}
	}

	// Use the raw builder when a join or projection has no single row type.
	rows, err := client.SelectFrom(users.Ref()).
		Join(query.InnerJoin(orders.Ref(), query.Equal(userID, orderUserID))).
		Project(query.Project(userID), query.Project(email)).
		Where(query.GreaterThan(total, query.Bind(20))).
		Order(query.Desc(total)).
		Query(ctx)
	if err != nil {
		fmt.Printf("failed to query order totals: %s\n", err)
		return
	}
	userIDValue, err := row.Get[int64](rows[0], "id")
	if err != nil {
		fmt.Printf("failed to read user ID: %s\n", err)
		return
	}
	emailValue, err := row.Get[string](rows[0], "email")
	if err != nil {
		fmt.Printf("failed to read email: %s\n", err)
		return
	}
	fmt.Println(userIDValue, emailValue)

	// Output:
	// 1 ada@example.com
}
```
source: [examples/runtime_dynamic_projection_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/runtime_dynamic_projection_example_test.go)
<!-- END INCLUDE -->

# Execute writes

Use `runtime.Insert` for a typed row. Build an immutable `query` statement for a custom update.

<!-- INCLUDE(examples/runtime_write_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/runtime"
)

func Example_runtime_write() {
	ctx := context.Background()
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
	if err := client.CreateTable(ctx, users.Ref().Table()); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	id, err := users.Ref().Column("id")
	if err != nil {
		fmt.Printf("failed to find users.id: %s\n", err)
		return
	}
	email, err := users.Ref().Column("email")
	if err != nil {
		fmt.Printf("failed to find users.email: %s\n", err)
		return
	}

	if _, err := runtime.Insert(ctx, client, users, UserRow{ID: 42, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// Write statements stay immutable. WithWhere returns the UPDATE statement
	// that client.Exec renders and executes with its bound values.
	update, err := query.NewUpdate(users.Ref(), query.Set(email, query.Bind("grace@example.com")))
	if err != nil {
		fmt.Printf("failed to build update: %s\n", err)
		return
	}
	update, err = update.WithWhere(query.Equal(id, query.Bind(42)))
	if err != nil {
		fmt.Printf("failed to add update predicate: %s\n", err)
		return
	}
	if _, err := client.Exec(ctx, update); err != nil {
		fmt.Printf("failed to update user: %s\n", err)
		return
	}

	user, err := runtime.SelectFrom(client, users).WhereEqual("id", 42).One(ctx)
	if err != nil {
		fmt.Printf("failed to query user: %s\n", err)
		return
	}
	fmt.Println(user.Email)

	// Output:
	// grace@example.com
}
```
source: [examples/runtime_write_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/runtime_write_example_test.go)
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

# Execute a static template

Pass the bound statement to `QueryRendered` to run it with the same client used for dynamic queries.

<!-- INCLUDE(examples/runtime_static_template_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/row"
	"github.com/lestrrat-go/rasql/runtime"
	querytemplate "github.com/lestrrat-go/rasql/template"
)

func Example_runtime_static_template() {
	ctx := context.Background()
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
	if err := client.CreateTable(ctx, users.Ref().Table()); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if _, err := runtime.Insert(ctx, client, users, UserRow{ID: 42, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	parsed, err := querytemplate.Parse("user_by_email", "SELECT id, email FROM users WHERE email = {{bind \"email\"}}")
	if err != nil {
		fmt.Printf("failed to parse template: %s\n", err)
		return
	}
	compiled, err := parsed.Compile(dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to compile template: %s\n", err)
		return
	}
	statement, err := compiled.Bind(map[string]any{"email": "ada@example.com"})
	if err != nil {
		fmt.Printf("failed to bind template: %s\n", err)
		return
	}

	// QueryRendered executes the dialect-specific statement produced by the template.
	rows, err := client.QueryRendered(ctx, statement)
	if err != nil {
		fmt.Printf("failed to query user: %s\n", err)
		return
	}
	email, err := row.Get[string](rows[0], "email")
	if err != nil {
		fmt.Printf("failed to read email: %s\n", err)
		return
	}
	fmt.Println(email)

	// Output:
	// ada@example.com
}
```
source: [examples/runtime_static_template_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/runtime_static_template_example_test.go)
<!-- END INCLUDE -->

The project requires Go 1.26 or newer and uses parameterized types where they improve type safety or avoid conversions.

The `schema`, `query`, `render`, `row`, and `runtime` packages cover the main application path. The `inspect` package normalizes live table columns and primary keys. The `generate` and `template` packages produce deterministic Go source.

`rasqlgen schema` reads PostgreSQL metadata with `-dsn` and `-table`, or accepts a JSON schema snapshot with `-input`. It generates typed `runtime.Table` descriptors with reusable `query.TableRef` values. `rasqlgen query` generates a parameterized Go function from a restricted SQL template. Both commands reject unchecked template actions and preserve values as bound arguments.

Runnable documentation examples live in [`examples/`](examples/) as executable Go examples.

See [DESIGN.md](DESIGN.md) for the architecture and focused implementation history.
