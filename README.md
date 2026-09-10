# rasql

`rasql` (pronounced “rascal”) is an all-in-one SQL toolkit for Go.

It gives an application one model for schema definitions, dynamic queries, static queries, result decoding, and database inspection. Every statement it produces is parameterized, so values travel as bound arguments and never as SQL text.

`rasql` comes in two layers that share one query and execution model.

* The **core layer** describes tables, builds SQL, compiles templates, inspects databases, and applies migrations.
* The **ORM layer** adds generated sources, projections, decoders, mutation plans, and graph plans. Every workflow executes through
  `rasql.Executor` and the same result terminals.

Most applications start with the ORM layer. [Getting started](docs/01-getting-started.md) installs the toolkit, describes a table, and runs a first query end to end, and [Querying](docs/02-querying.md) says which layer a given task calls for.

## Features

* **DDL migrations.** Run checked-in SQL migration directories in order with [`rasql migrate apply`](docs/core/07-migrations.md),
  reconcile interrupted MySQL work with [`rasql migrate reconcile`](docs/core/07-migrations.md), revert them with
  [`rasql migrate revert`](docs/core/07-migrations.md#revert-a-migration), and generate a PostgreSQL, MySQL, or SQLite
  migration from desired-schema sources when that helps. See [Migrations](docs/core/07-migrations.md).
* **Query builder.** The `query` package builds a dialect-neutral statement and validates it, and `render` turns that statement into SQL text with its arguments in placeholder order. Both packages import only `schema` and `dialect`, so this layer runs with no database handle and no Go row type. See [The SQL builder](docs/core/02-sql-builder.md).
* **ORM.** Run `rasql codegen generate` against a database to write typed rows, sources, projections, mutation builders,
  graph descriptors, and static queries as checked-in Go. Build reads with `rasql.Select`, writes with mutation plans, and consume
  results with `Rows`, `All`, `One`, or `Maybe`. See [`rasql codegen`](docs/orm/01-codegen.md),
  [Typed queries](docs/orm/03-typed-queries.md), and [Writing rows](docs/orm/04-writing.md).
* **Runtime-selected results.** Build a `ResultSchema` at run time and pass it to `DynamicProjection`, then use that projection with
  `Select` or `Native`. The standard result terminals return ordered row values without a second execution API.
* **Static query templates.** Compile SQL text with named binds into parameterized statements. See [Named SQL](docs/core/06-named-sql.md).
* **Schema description and inspection.** Write table definitions as Go code, or read them back from a live database. See [Schemas](docs/core/01-schema.md).
* **PostgreSQL, MySQL, and SQLite.** Engine profiles make supported syntax and limits explicit. The conformance suite records the
  portable cases exercised for each engine.

## Requirements

`rasql` requires Go 1.26 or newer. It builds on `database/sql`, so an application imports the driver it wants where it opens the connection.

## Install

```sh
go get github.com/lestrrat-go/rasql
```

The generator is a separate command, installed and run from the module root whenever the source database changes:

```sh
go run github.com/lestrrat-go/rasql/cmd/rasql codegen generate -dsn "$DATABASE_URL"
```

It reads the live metadata and writes the store package. The settings that stay the same from run to run live in a checked-in `rasql.json`, which [`rasql codegen`](docs/orm/01-codegen.md) covers, and [The generated store](docs/orm/02-generated-store.md) shows the files a run leaves behind.

## Sample application

The [Taskboard sample](sample/taskboard/) is an HTTP application on PostgreSQL, built on checked-in migrations and a generated store. Its page shows typed descriptors, a joined read, an insert, an update, and a compiled SQL query in one small application. It is a module of its own, `example.com/taskboard`, so it reaches rasql only through the public API a real project has.

Its [nine-chapter walkthrough](sample/taskboard/walkthrough/01-design.md) is how the code got there. It settles the schema against a running PostgreSQL server, captures it into a migration, generates the Go, writes the application on top, and then changes the schema and follows the compiler through what breaks.

Running it needs a PostgreSQL server and a database on it:

```sh
cd sample/taskboard
export TASKBOARD_DSN='postgres://rasql:rasql@127.0.0.1:5432/taskboard?sslmode=disable'
./scripts/migrate.sh apply
go run ./cmd/taskboard
```

`scripts/migrate.sh` applies the migrations with `rasql migrate apply`, and the application then serves the page over whatever rows the database holds. Open <http://127.0.0.1:8080/> in another terminal. [The sample's README](sample/taskboard/README.md) covers the rest, including the two rows to insert before the add form has a project and an owner to offer.

## Documentation

The [documentation index](docs/) groups these pages by layer: the core layer builds and runs SQL with no Go row type, and the ORM layer generates a store package and reads and writes Go values through it.

| Page | Covers |
| --- | --- |
| [Getting started](docs/01-getting-started.md) | Installing, creating a DB, and running a first query. |
| [Querying](docs/02-querying.md) | Portable queries, native SQL, projections, and result terminals. |
| **Core layer** | |
| [Schemas](docs/core/01-schema.md) | Describing tables in Go and reading them back from a live database. |
| [The SQL builder](docs/core/02-sql-builder.md) | Building and rendering a statement through `query` and `render`, with a reference for every constructor and predicate. |
| [Write statements](docs/core/03-write-statements.md) | Building inserts, updates, deletes, and upserts, and reading a `RETURNING` clause. |
| [The database handle](docs/core/04-database.md) | Running a rendered statement, installing hooks, and starting a transaction. |
| [Dynamic results](docs/core/05-dynamic.md) | Decoding a runtime-selected result schema through the canonical query API. |
| [Named SQL](docs/core/06-named-sql.md) | Compiling SQL text with named binds into parameterized statements. |
| [Migrations](docs/core/07-migrations.md) | Applying ordered DDL migrations, and reverting them. |
| [Inspection-only facts](docs/core/08-inspection-facts.md) | Reference for the facts inspection reads that rasql cannot write back as DDL. |
| **ORM layer** | |
| [`rasql codegen`](docs/orm/01-codegen.md) | Running the generator and configuring it with `rasql.json`. |
| [The generated store](docs/orm/02-generated-store.md) | The row types, table types, column accessors, and static query functions it writes. |
| [Typed queries](docs/orm/03-typed-queries.md) | Sources, typed expressions, projections, joins, and result terminals. |
| [Writing rows](docs/orm/04-writing.md) | Create, patch, delete, native, and batch mutation plans. |

The API reference lives at [pkg.go.dev](https://pkg.go.dev/github.com/lestrrat-go/rasql). Each code block that links to a source file is a runnable Go example from [`examples/`](examples/), verified by `go test`.

## How the packages fit together

Most applications only import the root `rasql` package plus `dialect` and `schema`. The rest are building blocks the root package uses on their behalf.

| Package | Responsibility |
| --- | --- |
| `rasql` | Defines typed queries, projections, mutation and graph plans, executors, and result terminals. |
| `schema` | Describes tables, columns, indexes, constraints, and logical types. |
| `dialect` | Decides identifier quoting, placeholders, type mapping, and syntax support. |
| `query` | Represents dialect-neutral statements and expressions, with validation. |
| `render` | Turns a validated query into SQL text and an ordered argument list. |
| `inspect` | Reads live database metadata into `schema` descriptors. |
| `catalog` | Reads a whole live catalog in one transaction and applies table selection. |
| `migrate` | Plans, executes, and reverts DDL migrations with durable history. |
| `namedsql`, `generate` | Compile templates and descriptors into deterministic Go source. |
| `cmd/rasql` | Scaffolds the generator program and applies migrations, as `rasql codegen` and `rasql migrate`. |
| `cmd/rasqlgen`, `cmd/rasqlmigrate` | Accept the same commands as the unified `rasql` command, under their own names. |

See [DESIGN.md](DESIGN.md) for the architecture and the reasoning behind these boundaries.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the local development workflow, including running the live-database tests.
