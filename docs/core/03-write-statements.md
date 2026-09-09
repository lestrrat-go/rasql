# Write statements

The `query` package builds dialect-neutral insert, update, delete, and upsert statements. Values become bind arguments,
identifiers come from validated table definitions, and each statement validates independently of a database handle.

## Insert

Use `query.NewInsert` for one assignment row, `query.NewInsertRows` for explicit columns and multiple rows, or
`query.Defaults` for a database-default row. The constructor rejects columns from another table, duplicate assignments,
mixed default/value rows, and invalid expressions.

## Update

Use `query.NewUpdate` with `query.Set` or `query.SetDefault`, then add a predicate with `WithWhere`. An update without a
predicate is rejected unless the caller explicitly uses `AllowAll`.

## Delete

Use `query.NewDelete`, then add a predicate with `WithWhere`. As with update, deleting every row requires an explicit
`AllowAll` call.

## Upsert

`query.NewUpsert` combines an insert with conflict columns and update assignments. Dialect profiles decide which conflict
forms and returning clauses are available when the statement is compiled.

## Execute a portable statement

Adapt any validated `query.WriteStatement` to the canonical mutation API:

<!-- INCLUDE(sample/taskboard/internal/store/docs_examples_test.go#statement_plan) -->
```go
tasks := Tasks().Table
statement, err := query.NewInsert(tasks.Ref(),
	query.Set(tasks.Column("project_id"), int64(1)),
	query.Set(tasks.Column("title"), "write the guide"),
)
if err != nil {
	return err
}
plan, err := rasql.NewStatementPlan(statement)
if err != nil {
	return err
}
outcome, err := rasql.ExecMutation(ctx, executor, plan)
```
source: [sample/taskboard/internal/store/docs_examples_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/docs_examples_test.go)
<!-- END INCLUDE -->

`ExecMutation` rejects a statement that already contains raw returning projections. Attach a typed rasql projection
through `rasql.Returning` instead, then use `Rows`, `All`, `One`, or `Maybe`:

<!-- INCLUDE(sample/taskboard/internal/store/docs_examples_test.go#mutation_returning) -->
```go
returned, err := rasql.Returning(plan, projection)
if err != nil {
	return err
}
saved, err := rasql.One(ctx, executor, returned)
```
source: [sample/taskboard/internal/store/docs_examples_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/docs_examples_test.go)
<!-- END INCLUDE -->

This keeps write-returning results on the same decoder, codec, cardinality, row lifecycle, and observation path as reads.

## Render without executing

Use `render.Write(dialect, statement)` when a tool needs SQL text and ordered arguments without a database call. Native
engine-specific SQL uses `rasql.NativeMutation` instead of passing unvalidated SQL through the portable builder.

`Executor.Exec` is the low-level boundary for an already compiled `stmt.Statement`. Application writes normally use
`ExecMutation`, which validates the plan and rejects raw `RETURNING` projections so callers decode them with `Returning`.

## Safety and errors

Construction errors leave the original immutable statement unchanged. Rasql reports invalid structure before execution,
and the engine still decides constraint results, affected-row counts, and final transaction durability.

## Next

[The SQL builder](02-sql-builder.md) covers expressions and portable reads. [Writing rows](../orm/04-writing.md) covers
generated mutation plans, optimistic versions, returning rows, and batches.
