# Database execution

`rasql.Open` pairs a `database/sql` handle with a dialect and resolves the engine profile a `rasql.DB` needs to run
queries and mutations: the compiler, bind limits, codecs, scopes, and event observers every query and mutation needs.
A `DB` from `Open` is itself a `rasql.Executor`, the canonical runtime type.

## Create an executor

<!-- INCLUDE(sample/taskboard/cmd/taskboard/main.go#open_database) -->
```go
config, err := pgx.ParseConfig(dsn)
if err != nil {
	return fmt.Errorf("parse TASKBOARD_DSN: %w", err)
}
database := stdlib.OpenDB(*config)
defer func() { _ = database.Close() }()

// Open pairs the handle with the dialect used to render SQL and asks the
// server its own version, so it works against whatever supported
// PostgreSQL release TASKBOARD_DSN actually points at.
executor, err := rasql.Open(context.Background(), database, dialect.PostgreSQL())
if err != nil {
	return fmt.Errorf("open the rasql database: %w", err)
}
```
source: [sample/taskboard/cmd/taskboard/main.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/cmd/taskboard/main.go)
<!-- END INCLUDE -->

`Open` asks the connected server its version and picks the built-in profile that matches, by default. Pass
`rasql.WithProfile` to pin a profile instead, either because the dialect names a custom engine that discovery cannot
resolve on its own, or because the handle cannot answer a version query. Rasql uses the resolved profile to reject
unsupported syntax and enforce actual bind limits before opening rows or executing a mutation.

## Run queries and mutations

Every `Query[R]` uses `Rows`, `All`, `One`, or `Maybe`. Every `MutationPlan` uses `Exec`, or
`ExecBatch` for an ordered batch. Native SQL enters through `Native` or `NativeMutation` and states its engine
identity explicitly.

The executor applies codecs and reports `PlanError`, `BindError`, or `DecodeError` with structured paths. Each of
those wraps the database or driver error it came from, so `errors.Is` and `errors.As` reach it.

## Transactions and savepoints

Use `rasql.Within` to execute a scope atomically:

<!-- INCLUDE(sample/taskboard/internal/store/docs_examples_test.go#transaction_scope) -->
```go
err := rasql.Within(ctx, executor, nil, func(ctx context.Context, scoped rasql.Executor) error {
	if _, err := rasql.Exec(ctx, scoped, first); err != nil {
		return err
	}
	_, err := rasql.Exec(ctx, scoped, second)
	return err
})
```
source: [sample/taskboard/internal/store/docs_examples_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/docs_examples_test.go)
<!-- END INCLUDE -->

A database executor starts a transaction. A transaction executor starts a savepoint when the engine supports it. The
scoped executor reuses the parent's compiler, engine profile, codecs, limits, and observers. A successful mutation inside
the scope reports pending durability until the outer transaction commits.

## Observe work

`WithEventObservers` decorates an executor. An observer receives paired start and terminal events for statements,
mutation batches, graphs, and scopes. Logical IDs connect nested work, statement indexes preserve order, and terminal
events report rows, early closure, and errors.

Extension errors do not erase successful database evidence. The returned error records whether execution succeeded,
while a mutation outcome still reports its affected rows and the durability the executor can prove.

## Low-level handles

`QueryRendered` and the transaction methods (`Begin`, `Commit`, `Rollback`) run against the handle and dialect alone,
skipping the capability checks and result lifecycle the typed `Query[R]`, `MutationPlan`, and `Native` paths apply
through `Executor`. Application ORM code should use those typed paths instead.

## Next

[Querying](../02-querying.md) covers the common result terminals. [Writing rows](../orm/04-writing.md) covers mutation
plans and batches. [Migrations](07-migrations.md) covers durable schema changes and their separate history model.
