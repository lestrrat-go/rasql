# rasql documentation

These pages explain how to use `rasql` in an application. For what `rasql` is and how to install it, start with the [project README](../README.md). For the API reference, see [pkg.go.dev](https://pkg.go.dev/github.com/lestrrat-go/rasql).

## Reading order

1. [Getting started](01-getting-started.md) creates a DB and runs a first query end to end.
2. [Schemas](02-schema.md) describes tables in Go code, and reads existing tables back from a database.
3. [Querying](03-querying.md) reads rows: typed selects, joins, custom projections, and SQL inspection. Its [operation reference](03-querying.md#operation-reference) enumerates every statement, builder method, and predicate. `rasql` is the typed layer; `rasql/dynamic` holds the builders and row reads for columns you only know as strings when the program runs.
4. [Writing rows](04-writing.md) creates tables and inserts, updates, or deletes rows, including inside a [transaction](04-writing.md#transactions).
5. [Static templates](05-templates.md) compiles fixed SQL text with named binds.
6. [`rasql codegen`](06-rasqlgen.md) writes the store package from live metadata, either as a command or through a project-owned generator.
7. [Migrations](07-migrations.md) applies forward-only DDL migrations.

Pages 3 through 5 are independent of each other. Read the first two, then jump to whichever fits the task.

For a new application, start with the `rasql codegen generate` command in [the generator guide](06-rasqlgen.md#generate-run-the-command). A project that needs Go-side hints or static queries owns a generator program instead, which `rasql codegen init` scaffolds; the checked-in `gen/main.go` then keeps the driver, table selection, hints, queries, pruning policy, and drift check in the application repository.

## About the code blocks

Each Go block that links to a source file is copied from a runnable example in [`examples/`](../examples/). `go test ./examples/` runs each one and compares its output, and `TestDocExamplesMatchSource` fails if a page drifts from its source file. Shorter blocks without a link illustrate one call and are not executed. After editing an example, refresh the pages with:

```sh
go test ./examples/ -update-docs
```

Never edit a linked block by hand. The next update run overwrites it.
