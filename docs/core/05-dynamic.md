# Dynamic results

`rasql.DynamicProjection[R]` decodes a result contract assembled at run time without adding a second query builder or
execution path. Use it when column metadata comes from configuration, a report definition, or another runtime source,
while the application still has a Go struct for the returned shape.

## Define the contract

Create a `rasql.ResultSchema` from ordered `ResultColumn` values. Each column states its name, SQL type, NULL behavior,
and optional codec. `DynamicProjection[R]` matches those names to exported fields on `R` by `rasql` tag, `json` tag, or
snake-cased field name. Construction fails before database access when a field is missing, duplicated, unexported, or
incompatible with the declared SQL type.

<!-- INCLUDE(sample/taskboard/internal/store/docs_examples_test.go#dynamic_projection) -->
```go
result, err := rasql.NewResultSchema(
	rasql.ResultColumn{Name: "display_name", Type: schema.TextType{}},
	rasql.ResultColumn{Name: "open_tasks", Type: schema.IntegerType{}},
)
if err != nil {
	return err
}
projection, err := rasql.DynamicProjection[docsReportRow](result)
```
source: [sample/taskboard/internal/store/docs_examples_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/docs_examples_test.go)
<!-- END INCLUDE -->

Nullable columns require a nullable Go destination such as a pointer, slice, map, interface, `sql.Scanner`, or rasql
nullable value. SQL NULL bypasses value codecs and keeps the destination absent.

## Execute it

Pass the projection to `rasql.Select` for a portable query or to `rasql.Native` for engine-specific SQL. Execute the
resulting `Query[ReportRow]` with `Rows`, `All`, `One`, or `Maybe`, just like a generated projection.

<!-- INCLUDE(sample/taskboard/internal/store/docs_examples_test.go#dynamic_native_query) -->
```go
q, err := rasql.Native(
	rasql.NativeStatement{Engine: "postgresql", SQL: reportSQL, Args: args},
	projection,
	rasql.Many,
)
if err != nil {
	return err
}
rows, err := rasql.All(ctx, executor, q)
```
source: [sample/taskboard/internal/store/docs_examples_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/docs_examples_test.go)
<!-- END INCLUDE -->

The declared result names can arrive in a different order. The runtime binds returned column metadata to the schema,
rejects missing or unknown columns, and applies codecs by the returned position.

## When to use generated projections

Prefer generated projections when the schema is known during generation. They provide typed column expressions and
avoid reflection when decoding. `DynamicProjection` is the boundary for runtime metadata; it does not accept arbitrary
maps and does not make native SQL portable.

## Next

[Querying](../02-querying.md) explains portable and native queries. [Named SQL](06-named-sql.md) covers compiling static
SQL templates, including generated native query functions.
