# Typed queries

Generated stores expose typed sources, expressions, projections, decoders, page keys, and graph keys. These values feed
the root `rasql.Query[R]` API directly; generated code does not add another query builder or execution loop.

## Build a query

Bind a generated column set to a source, build its generated projection, and pass both to `rasql.Select`:

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

`Select` is immutable. `Where`, `Join`, `GroupBy`, `Having`, `OrderBy`, `Distinct`, `Limit`, and `Offset` return a new
query, so a validated base value can be reused safely. Validation reports sticky construction errors before execution.

## Projections

A `Projection[R]` owns the ordered SQL expressions, declared result schema, presence rules, and row decoder for `R`.
Generated full-row projections are the common case. Build a custom projection with `NewProjection`, `Item`, `NullItem`,
and a `RowDecoder[R]` when a join or aggregate returns another shape.

Use `Scalar` for a single value. Use `DynamicProjection` for a result schema assembled at run time. Use
`NativeProjection` when a generated or hand-written decoder already declares its own result schema.

Changing the selected shape means creating another projection. A query never keeps an old result type after replacing
its SQL projection.

## Joins and optional rows

`InnerJoin` binds another required source. `LeftJoin` takes an optional relation and its projection uses nullable
expressions plus presence metadata. That metadata distinguishes an absent child row from a present row whose individual
columns are NULL.

Generated graph descriptors build on the same sources and projections. Use `NewGraphPlan` with `LoadGraph` for related
rows, or `PageGraphAfter` for keyset pages with relationships. Per-parent limits are lowered into database queries.

## Result terminals

All query kinds use the same terminals:

| Terminal | Result |
| --- | --- |
| `Rows` | A lazy sequence of decoded rows and errors. |
| `All` | Every row in order. |
| `One` | Exactly one row, otherwise `ErrNoRows` or `ErrMultipleRows`. |
| `Maybe` | Zero or one row plus a presence flag. |

The executor applies bind and result codecs, checks engine capabilities, records events, and closes rows on exhaustion,
early termination, cancellation, or error. A transaction or savepoint executor uses the same query value and terminals.

## Native and generated static queries

`rasql.Native` accepts engine-specific SQL, a projection, and a declared cardinality. Generated static query functions
return this same `Query[R]` shape, so callers keep the normal result terminals and error behavior.

## Next

[The generated store](02-generated-store.md) explains emitted sources and projections. [Writing rows](04-writing.md)
covers mutation plans, returning rows, and batches. [Dynamic results](../core/05-dynamic.md) covers runtime schemas.
