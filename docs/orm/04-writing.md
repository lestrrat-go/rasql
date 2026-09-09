# Writing rows

Rasql writes through immutable `MutationPlan` values and an `Executor`. Generated create and patch builders validate
required fields, writable columns, NULL state, database defaults, and predicates before any database call.

## Create

A generated create builder distinguishes four states: omitted, explicit value including a zero value, SQL NULL for a
nullable column, and database default. Its `Plan` method returns a `CreatePlan[R]`.

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

`MutationOutcome.Affected` is the driver's affected-row count. Its durability is committed for a successful autocommit,
pending inside a caller-owned transaction or savepoint, and unknown when the executor cannot prove the final state.

## Patch

A generated patch builder requires a typed predicate and emits assignments only for fields explicitly selected by the
caller. Build and execute it through the same mutation terminal:

<!-- INCLUDE(sample/taskboard/internal/store/docs_examples_test.go#canonical_patch) -->
```go
source, err := Tasks().Source("")
if err != nil {
	return err
}
expressions, err := (TasksColumns{}).Bind(source)
if err != nil {
	return err
}
plan, err := NewTasksPatch().IsOpen(false).
	Where(rasql.EqualValue(expressions.ID.Expr(), taskID))
if err != nil {
	return err
}
outcome, err := rasql.ExecMutation(ctx, executor, plan)
```
source: [sample/taskboard/internal/store/docs_examples_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/docs_examples_test.go)
<!-- END INCLUDE -->

`PatchPlan.WithVersion` adds one optimistic version predicate and increment. A zero-row result reports
`ErrPrecondition`; more than one row reports `ErrMultipleRows`.

## Delete and portable statements

Use `NewDeletePlan` with a table and typed predicate. Use `NewStatementPlan` to adapt an already validated portable
`query.Insert`, `query.Update`, `query.Delete`, or `query.Upsert`. Use `NativeMutation` for engine-specific SQL and state
the engine explicitly. Every form executes through `ExecMutation`.

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

`ExecMutationBatch` accepts ordered mutation plans and `BulkOptions`. Consecutive compatible creates may share one
insert statement while row and bind limits remain enforced. The outcome reports each input separately as unattempted,
applied, rolled back, rejected, or unknown, plus the failed batch indexes and overall durability.

<!-- INCLUDE(sample/taskboard/internal/store/docs_examples_test.go#mutation_batch) -->
```go
outcome, err := rasql.ExecMutationBatch(ctx, executor, plans, rasql.BulkOptions{
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
