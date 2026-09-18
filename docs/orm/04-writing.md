# Writing rows

Rasql writes through immutable `MutationPlan` values and an `Executor`. Generated create, patch and delete builders
validate required fields, writable columns, NULL state, database defaults, and predicates before any database call.
Each one carries a terminal `Exec` method that builds the plan and runs it in one call, so the common path costs one
error check instead of two.

## Create

A generated create builder distinguishes four states: omitted, explicit value including a zero value, SQL NULL for a
nullable column, and database default. Its `Exec` method plans and runs the insert in one call.

<!-- INCLUDE(sample/taskboard/internal/store/docs_examples_test.go#canonical_create) -->
```go
outcome, err := Tasks().Create().
	ProjectID(projectID).
	ClearAssigneeID().
	Title("document canonical mutations").
	DefaultIsOpen().
	DefaultCreatedAt().
	Exec(ctx, executor)
```
source: [sample/taskboard/internal/store/docs_examples_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/docs_examples_test.go)
<!-- END INCLUDE -->

`MutationOutcome.Affected` is the driver's affected-row count. Its durability is committed for a successful autocommit,
pending inside a caller-owned transaction or savepoint, and unknown when the executor cannot prove the final state.

## Patch

A generated patch builder requires a typed predicate and emits assignments only for fields explicitly selected by the
caller. `Where` stores the predicate and returns the builder, so it chains like any other step; `Exec` plans and runs
the update in one call:

<!-- INCLUDE(sample/taskboard/internal/store/docs_examples_test.go#canonical_patch) -->
```go
expressions, err := (TasksColumns{}).Bind(Tasks())
if err != nil {
	return err
}
outcome, err := Tasks().Patch().IsOpen(false).
	Where(rasql.EqualValue(expressions.ID.Expr(), taskID)).
	Exec(ctx, executor)
```
source: [sample/taskboard/internal/store/docs_examples_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/docs_examples_test.go)
<!-- END INCLUDE -->

`PatchPlan.WithVersion` adds one optimistic version predicate and increment. A zero-row result reports
`ErrPrecondition`; more than one row reports `ErrMultipleRows`.

## Delete and portable statements

A generated table's `Delete` method takes no argument and hands back a delete builder; its `Where` stores a typed
predicate and returns the builder, matching the patch builder's shape, and its `Exec` plans and runs the delete in
one call: `Tasks().Delete().Where(predicate).Exec(ctx, executor)`.

Use `NewStatementPlan` to adapt an already validated portable `query.Insert`, `query.Update`, `query.Delete`, or
`query.Upsert`. Use `NativeMutation` for engine-specific SQL and state the engine explicitly. Every form executes
through `rasql.Exec`.

Every builder also keeps a `Plan` method that returns the plan without running it. It stays the way to reach
`rasql.ExecBatch`, `rasql.Returning`, or `render` without executing, and every builder carries an error raised by any
of its steps through to whichever of `Plan` or `Exec` is called.

## Returning rows

`Returning` attaches a `Projection[R]` to a mutation plan and returns a normal `Query[R]`:

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

This path uses the normal codecs, cardinality checks, row lifecycle, and event observation. Dialects without `RETURNING`
fail during planning rather than starting a second write-and-read workflow.

## Batches

`ExecBatch` accepts ordered mutation plans and `BulkOptions`. Consecutive compatible creates may share one
insert statement while row and bind limits remain enforced. The outcome reports each input separately as unattempted,
applied, rolled back, rejected, or unknown, plus the failed batch indexes and overall durability.

<!-- INCLUDE(sample/taskboard/internal/store/docs_examples_test.go#mutation_batch) -->
```go
outcome, err := rasql.ExecBatch(ctx, executor, plans, rasql.BulkOptions{
	MaxRows:           500,
	MaxBindParameters: 32000,
	Atomic:            true,
})
```
source: [sample/taskboard/internal/store/docs_examples_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/docs_examples_test.go)
<!-- END INCLUDE -->

With `Atomic`, rasql owns a transaction or savepoint and changes applied inputs to rolled back after a confirmed
rollback. Cleanup or commit uncertainty marks affected inputs unknown. A `BatchFailureClassifier` may identify a
confirmed constraint rejection; transport and cleanup failures remain unknown.

## Transactions

Use `rasql.Within` to run queries and mutations in a transaction or nested savepoint. The callback receives an executor
with the same compiler, codecs, limits, and observers as its parent.

## Next

[Typed queries](03-typed-queries.md) covers projections and result terminals. [Write statements](../core/03-write-statements.md)
covers the lower-level portable statement model. [Migrations](../core/07-migrations.md) covers database schema changes.
