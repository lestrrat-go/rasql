# rasql

`rasql` (pronounced “rascal”) is an all-in-one SQL toolkit for Go.

It gives an application one model for schema definitions, dynamic queries, static queries, result decoding, and database inspection. Every statement it produces is parameterized: values travel as bound arguments, never as SQL text.

* PostgreSQL, MySQL, SQLite, and Google Cloud Spanner dialects.
* Schema definitions written as Go code, including generation from live database metadata.
* Type-safe result-set access.
* Dynamic query building at runtime.
* Static query building with templates.

`rasql` does not plan or run migrations. It describes and inspects schemas, and provides the pieces a migration tool can build on.

## Requirements

`rasql` requires Go 1.26 or newer. It builds on `database/sql`, so an application imports the driver it wants where it opens the connection.

## Install

```sh
go get github.com/lestrrat-go/rasql
```

## Quick start

This program describes a table, creates it in an in-memory SQLite database, writes a row, and reads it back as a Go value.

<!-- INCLUDE(examples/rasql_sqlite_query_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_sqlite_query() {
	// This example creates, inserts, and reads one generated row with SQLite.
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
	// Create the schema described by the generated table reference.
	if err := rasql.Create(ctx, client, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	// Insert encodes UserRow's tagged fields as bound values.
	if _, err := rasql.Insert(ctx, client, users, UserRow{ID: 42, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// users is a typed table descriptor with the shape emitted by rasqlgen.
	user, err := rasql.SelectFrom(client, users).WhereEqual("id", 42).One(ctx)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}

	fmt.Println(user.Email)

	// Output:
	// ada@example.com
}
```
source: [examples/rasql_sqlite_query_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_sqlite_query_example_test.go)
<!-- END INCLUDE -->

`users` is a typed table descriptor with the shape `rasqlgen` emits for a real table. See [Getting started](docs/01-getting-started.md) for its definition and for the steps this example compresses.

## Documentation

| Page | Covers |
| --- | --- |
| [Getting started](docs/01-getting-started.md) | Installing, creating a client, and running a first query. |
| [Schemas](docs/02-schema.md) | Describing tables in Go and reading them back from a live database. |
| [Querying](docs/03-querying.md) | Typed selects, joins, custom projections, and seeing the generated SQL. |
| [Writing rows](docs/04-writing.md) | Creating tables and inserting or updating typed rows. |
| [Static templates](docs/05-templates.md) | Compiling SQL text with named binds into parameterized statements. |
| [`rasqlgen`](docs/06-rasqlgen.md) | Generating Go source from a database, a schema snapshot, or a template. |

The API reference lives at [pkg.go.dev](https://pkg.go.dev/github.com/lestrrat-go/rasql). Every code block in this repository's documentation is a runnable Go example from [`examples/`](examples/), verified by `go test`.

## How the packages fit together

Most applications only import the root `rasql` package plus `dialect` and `schema`. The rest are building blocks the root package uses on their behalf.

| Package | Responsibility |
| --- | --- |
| `rasql` | Executes statements, decodes typed rows, and provides the fluent API. |
| `schema` | Describes tables, columns, indexes, constraints, and logical types. |
| `dialect` | Decides identifier quoting, placeholders, type mapping, and syntax support. |
| `query` | Represents dialect-neutral statements and expressions, with validation. |
| `render` | Turns a validated query into SQL text and an ordered argument list. |
| `row` | Provides typed column access and result decoding. |
| `inspect` | Reads live database metadata into `schema` descriptors. |
| `template`, `generate`, `cmd/rasqlgen` | Compile templates and descriptors into deterministic Go source. |

See [DESIGN.md](DESIGN.md) for the architecture and the reasoning behind these boundaries.
