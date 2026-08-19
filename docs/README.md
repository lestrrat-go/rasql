# rasql documentation

These pages explain how to use `rasql` in an application. For what `rasql` is and how to install it, start with the [project README](../README.md). For the API reference, see [pkg.go.dev](https://pkg.go.dev/github.com/lestrrat-go/rasql).

## Reading order

1. [Getting started](01-getting-started.md) creates a DB and runs a first query end to end.
2. [Schemas](02-schema.md) describes tables in Go code, and reads existing tables back from a database.
3. [Querying](03-querying.md) says what the two builders are and which one a task calls for.
4. [The SQL builder](04-sql-builder.md) builds and renders a statement with `query` and `render`, with no database handle and no Go row type.
5. [Typed queries](05-typed-queries.md) reads rows through the generated table: typed selects, joins, custom projections, and SQL inspection. `rasql/dynamic` sits here too, for columns you only know as strings when the program runs.
6. [Writing rows](06-writing.md) creates tables and inserts, updates, or deletes rows, including inside a [transaction](06-writing.md#transactions).
7. [Static templates](07-templates.md) compiles fixed SQL text with named binds.
8. [`rasql codegen`](08-rasqlgen.md) writes the store package from live metadata, driven by one settings file.
9. [Migrations](09-migrations.md) applies ordered DDL migrations and reverts them.

Pages 4 through 7 stand on their own. Read the first three, then jump to whichever fits the task.

For a new application, start with the `rasql codegen generate` command in [the generator guide](08-rasqlgen.md#run-the-command). The checked-in [`rasql.json`](08-rasqlgen.md#the-settings-file) keeps the package name, output directory, table selection, row-type names, static queries, and pruning policy in the application repository.

## About the code blocks

Each Go block that links to a source file is copied from a runnable example in [`examples/`](../examples/). `go test ./examples/` runs each one and compares its output, and `TestDocExamplesMatchSource` fails if a page drifts from its source file. Shorter blocks without a link illustrate one call and are not executed. After editing an example, refresh the pages with:

```sh
go test ./examples/ -update-docs
```

Never edit a linked block by hand. The next update run overwrites it.
