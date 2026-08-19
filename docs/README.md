# rasql documentation

These pages explain how to use `rasql` in an application. For what `rasql` is and how to install it, start with the [project README](../README.md). For the API reference, see [pkg.go.dev](https://pkg.go.dev/github.com/lestrrat-go/rasql).

## Two layers

`rasql` is two layers, and these pages are grouped the same way. The core layer describes tables, builds and renders SQL, runs statements, compiles templates, and applies migrations, all without a Go row type. The ORM layer sits on top: `rasql codegen` writes a store package from the live database, and the typed builders read and write Go values through it.

Start with these two, whichever layer the task belongs to:

1. [Getting started](01-getting-started.md) creates a DB and runs a first query end to end.
2. [Querying](02-querying.md) says what the two builders are and which one a task calls for.

### The core layer

1. [Schemas](core/01-schema.md) describes tables in Go code, and reads existing tables back from a database.
2. [The SQL builder](core/02-sql-builder.md) builds and renders a statement with `query` and `render`, with no database handle and no Go row type.
3. [Write statements](core/03-write-statements.md) builds inserts, updates, deletes, and upserts the same way, and reads a `RETURNING` clause back.
4. [The database handle](core/04-database.md) runs a rendered statement, installs hooks, and starts a [transaction](core/04-database.md#transactions).
5. [Dynamic rows](core/05-dynamic.md) reads and writes rows for a column name the program only knows as a string.
6. [Static templates](core/06-templates.md) compiles fixed SQL text with named binds.
7. [Migrations](core/07-migrations.md) applies ordered DDL migrations and reverts them.

### The ORM layer

1. [`rasql codegen`](orm/01-codegen.md) writes the store package from live metadata, driven by one settings file.
2. [The generated store](orm/02-generated-store.md) says what that package contains: row types, table types, column accessors, and static query functions.
3. [Typed queries](orm/03-typed-queries.md) reads rows through the generated table: typed selects, joins, custom projections, and SQL inspection.
4. [Writing rows](orm/04-writing.md) creates tables and inserts, updates, or deletes rows.

Within each layer the pages stand on their own. Read the first two, then jump to whichever fits the task.

For a new application, start with the `rasql codegen generate` command in [the generator guide](orm/01-codegen.md#run-the-command). The checked-in [`rasql.json`](orm/01-codegen.md#the-settings-file) keeps the package name, output directory, table selection, row-type names, static queries, and pruning policy in the application repository.

## About the code blocks

Each Go block that links to a source file is copied from a runnable example in [`examples/`](../examples/). `go test ./examples/` runs each one and compares its output, and `TestDocExamplesMatchSource` fails if a page drifts from its source file. Shorter blocks without a link illustrate one call and are not executed. After editing an example, refresh the pages with:

```sh
go test ./examples/ -update-docs
```

Never edit a linked block by hand. The next update run overwrites it.
