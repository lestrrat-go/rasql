# Database execution

A `rasql.DB` pairs a `database/sql` handle with a dialect. The canonical runtime uses `rasql.Executor`, which adds an
engine profile and retains the compiler, bind limits, codecs, scopes, and event observers needed by every query and
mutation.

## Create an executor

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

The profile must describe the connected engine. Rasql uses it to reject unsupported syntax and enforce actual bind
limits before opening rows or executing a mutation.

## Run queries and mutations

Every `Query[R]` uses `Rows`, `All`, `One`, or `Maybe`. Every `MutationPlan` uses `ExecMutation`, or
`ExecMutationBatch` for an ordered batch. Native SQL enters through `Native` or `NativeMutation` and states its engine
identity explicitly.

The executor applies codecs and reports `PlanError`, `BindError`, or `DecodeError` with structured paths. Database and
driver errors remain available through error wrapping.

## Transactions and savepoints

Use `rasql.Within` to execute a scope atomically:

<!-- INCLUDE(sample/taskboard/internal/store/docs_examples_test.go#transaction_scope) -->
```go
err := rasql.Within(ctx, executor, nil, func(ctx context.Context, scoped rasql.Executor) error {
	if _, err := rasql.ExecMutation(ctx, scoped, first); err != nil {
		return err
	}
	_, err := rasql.ExecMutation(ctx, scoped, second)
	return err
})
```
source: [sample/taskboard/internal/store/docs_examples_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/docs_examples_test.go)
<!-- END INCLUDE -->

A database executor starts a transaction. A transaction executor starts a savepoint when the engine supports it. The
scoped executor retains the parent compiler, engine profile, codecs, limits, and observers. A successful mutation inside
the scope reports pending durability until the outer transaction commits.

## Observe work

`WithEventObservers` decorates an executor. An observer receives paired start and terminal events for statements,
mutation batches, graphs, and scopes. Logical IDs connect nested work, statement indexes preserve order, and terminal
events report rows, early closure, and errors.

Extension errors do not erase successful database evidence. The returned error records whether execution succeeded,
while mutation outcomes retain affected rows and the durability that the executor can prove.

## Low-level handles

The lower-level `DB` and `exec.DB` APIs remain implementation building blocks for inspection, migration, and compiler
packages. Application ORM code should use `Executor` so every operation receives the same capability checks and result
lifecycle.

## Next

[Querying](../02-querying.md) covers the common result terminals. [Writing rows](../orm/04-writing.md) covers mutation
plans and batches. [Migrations](07-migrations.md) covers durable schema changes and their separate history model.
