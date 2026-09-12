# Named SQL

The `namedsql` package compiles checked SQL templates whose bind arguments have stable names. It is useful for recursive
queries, window functions, vendor syntax, and other statements outside the portable builder.

## Template syntax

A template may contain ordinary SQL and bind actions:

```sql
SELECT id, email
FROM users
WHERE status = {{bind "status"}}
  AND created_at >= {{bind "since" users.created_at}}
ORDER BY id
```

`{{bind "name"}}` declares a named argument. The optional table and column reference records which column the argument
stands for, for a reader of the SQL; the generated parameter's Go type comes from the query entry's own `scalar`
declaration. Template actions cannot execute Go code or interpolate SQL fragments.

Compilation chooses placeholders for the target dialect and returns SQL text plus ordered bind metadata. Repeated names
remain repeated placeholders with one caller-visible argument identity.

## Generated static queries

List templates in `rasql.json` with an engine, operation, cardinality, parameter types, result columns, function name,
and generated output file. `rasql codegen generate` validates the declared contract and emits a checked-in function that
returns `Query[R]` for reads or `MutationPlan` for writes.

Generated read functions use `rasql.Native` with a generated projection and decoder. Callers use the same `Rows`, `All`,
`One`, or `Maybe` terminal as portable queries:

<!-- INCLUDE(sample/taskboard/internal/store/docs_examples_test.go#named_query) -->
```go
q, err := OverdueCount(today)
if err != nil {
	return err
}
row, err := rasql.One(ctx, executor, q)
```
source: [sample/taskboard/internal/store/docs_examples_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/docs_examples_test.go)
<!-- END INCLUDE -->

Generated mutations use `NativeMutation` and execute through `ExecMutation`. Native statements state their engine, so an
executor for another engine fails before database access.

## Direct native SQL

For hand-written SQL outside generation, create a projection and call `rasql.Native`:

<!-- INCLUDE(sample/taskboard/internal/store/docs_examples_test.go#direct_native_query) -->
```go
q, err := rasql.Native(
	rasql.NativeStatement{
		Engine: "postgresql",
		SQL:    "SELECT count(*) AS total FROM tasks WHERE due_on < $1",
		Args:   []rasql.NativeArgument{{Value: dueBefore}},
	},
	projection,
	rasql.ExactlyOne,
)
```
source: [sample/taskboard/internal/store/docs_examples_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/docs_examples_test.go)
<!-- END INCLUDE -->

Rasql does not parse native SQL or make it portable. The declared engine, result schema, bind codecs, cardinality, and
returned column metadata provide the runtime checks that are possible without changing the statement.

## Reproducible generation

`rasql.sum`, written beside the generated Go, records one line per setting group, migration,
file-backed query, and generated output. Run `rasql codegen check` in CI to prove the checked-in
package still matches those recorded inputs, and `rasql codegen check -dsn` where a database is
reachable to prove it against the database itself.

## Next

[Querying](../02-querying.md) explains native and portable queries. [The generated store](../orm/02-generated-store.md)
covers emitted query functions. [Dynamic results](05-dynamic.md) covers runtime-defined result schemas.
