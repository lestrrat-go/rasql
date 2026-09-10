# Getting started

Rasql builds typed queries and mutations over `database/sql`. Install it on the current module path:

```sh
go get github.com/lestrrat-go/rasql
```

Import a database driver in the application that opens the connection.

## Open an executor

A `rasql.DB` pairs a database handle with a dialect. An `Executor` also carries the engine profile required for query
capabilities, bind limits, codecs, scopes, and result decoding.

<!-- INCLUDE(sample/taskboard/cmd/taskboard/main.go#open_database) -->
```go
config, err := pgx.ParseConfig(dsn)
if err != nil {
	return fmt.Errorf("parse TASKBOARD_DSN: %w", err)
}
database := stdlib.OpenDB(*config)
defer func() { _ = database.Close() }()

// A rasql.DB pairs the handle with the dialect used to render SQL.
db, err := rasql.New(database, dialect.PostgreSQL())
if err != nil {
	return fmt.Errorf("create the rasql db: %w", err)
}
profile, err := rasql.DiscoverEngineProfile(context.Background(), db, "postgresql-17")
if err != nil {
	return fmt.Errorf("discover PostgreSQL engine profile: %w", err)
}
executor, err := rasql.AsExecutor(db, profile)
if err != nil {
	return fmt.Errorf("create the rasql executor: %w", err)
}
```
source: [sample/taskboard/cmd/taskboard/main.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/cmd/taskboard/main.go)
<!-- END INCLUDE -->

Choose the profile that matches the connected engine. It makes supported syntax explicit before rasql renders or runs
a statement.

## Generate a store

Create a checked-in `rasql.json` naming the dialect, generated package, output directory, and compact emitter.
Then run:

```sh
go run github.com/lestrrat-go/rasql/cmd/rasql codegen generate -dsn "$DATABASE_URL"
```

The command inspects the live database named by `-dsn`. It writes typed tables, sources,
column expressions, projections, decoders, mutation builders, graph descriptors, and static query functions. Commit the
generated files and use `rasql codegen check` in CI to detect drift.

## Read rows

<!-- INCLUDE(sample/taskboard/internal/store/docs_examples_test.go#canonical_read) -->
```go
source, err := Tasks().Source("tasks")
if err != nil {
	return err
}
expressions, err := (TasksColumns{}).Bind(source)
if err != nil {
	return err
}
projection, err := TasksProjection(expressions)
if err != nil {
	return err
}
q := rasql.Select(source.Source(), projection).
	Where(rasql.EqualValue(expressions.IsOpen.Expr(), true)).
	OrderBy(rasql.AscExpr(expressions.ID.Expr()))
rows, err := rasql.All(ctx, executor, q)
```
source: [sample/taskboard/internal/store/docs_examples_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/docs_examples_test.go)
<!-- END INCLUDE -->

The query owns the result type and decoder. `Rows`, `All`, `One`, and `Maybe` provide the four result cardinalities and
share one lifecycle path.

## Write rows

Generated mutation builders create immutable plans:

<!-- INCLUDE(sample/taskboard/internal/store/docs_examples_test.go#canonical_create) -->
```go
plan, err := NewTasksCreate().
	ProjectID(projectID).
	ClearAssigneeID().
	Title("document canonical mutations").
	DefaultIsOpen().
	DefaultCreatedAt().
	Plan()
if err != nil {
	return err
}
outcome, err := rasql.ExecMutation(ctx, executor, plan)
```
source: [sample/taskboard/internal/store/docs_examples_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/docs_examples_test.go)
<!-- END INCLUDE -->

Use `Returning(plan, projection)` to read inserted, updated, or deleted rows with the normal query terminals. Use
`ExecMutationBatch` with `BulkOptions` for ordered batches and per-input outcomes.

## Native and runtime-defined results

Use `Native` for engine-specific SQL and state its engine, projection, and cardinality. Use `DynamicProjection` when the
result schema is assembled at run time. Both return a normal `Query[R]` and use the same executor and terminals.

## Next

[Querying](02-querying.md) explains the common query model. [`rasql codegen`](orm/01-codegen.md) covers schema sources
and configuration. [The generated store](orm/02-generated-store.md) describes generated APIs. [Migrations](core/07-migrations.md)
covers durable database schema changes.
