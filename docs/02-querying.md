# Querying

Every rasql read ends as a `Query[R]` and runs through an `Executor`. The query carries its result projection, so the
same `Rows`, `All`, `One`, and `Maybe` terminals run portable builder queries, generated static queries, native SQL,
and runtime-selected result shapes.

## Portable queries

A generated table exposes a typed source and column expressions. Build a projection for the result type, pass both to
`rasql.Select`, and add predicates, joins, grouping, order, or limits immutably. The query validates source membership,
NULL behavior, result names, codecs, and engine capabilities before it opens database rows.

The lower-level `query` package builds dialect-neutral SQL of its own, and `render` turns those values
into SQL plus ordered arguments. It is useful for migration tooling and other code that needs SQL without a result type
or database handle.

Correlated subqueries declare their outer sources explicitly, so validation can distinguish an intended outer reference
from an accidental reference to an unrelated table:

<!-- INCLUDE(examples/query_correlated_projection_example_test.go#correlated_projection) -->
```go
func Example_query_correlated_projection() {
	users := query.MustTableRef(schema.MustTableDef("users", schema.Integer("id")))
	orders := query.MustTableRef(schema.MustTableDef(
		"orders",
		schema.Integer("id"), schema.Integer("user_id"), schema.Integer("amount"),
	))

	// The constructor declares users before it validates the projection, so the
	// projection can read both the order and the enclosing user's columns.
	ordersForUser, err := query.NewCorrelatedSelect(
		orders, []query.RelationSource{users},
		query.Project(query.Coalesce(orders.Column("amount"), users.Column("id"))).As("value"),
	)
	if err != nil {
		fmt.Printf("failed to build correlated select: %s\n", err)
		return
	}
	ordersForUser, err = ordersForUser.WithWhere(query.Equal(orders.Column("user_id"), users.Column("id")))
	if err != nil {
		fmt.Printf("failed to add correlation predicate: %s\n", err)
		return
	}
	statement, err := query.NewSelect(users, users.Column("id"), query.Project(query.Scalar(ordersForUser)).As("value"))
	if err != nil {
		fmt.Printf("failed to build outer select: %s\n", err)
		return
	}
	rendered, err := render.Select(dialect.SQLite(), statement)
	if err != nil {
		fmt.Printf("failed to render select: %s\n", err)
		return
	}
	fmt.Println(rendered.SQL())

	// Output:
	// SELECT "users"."id", (SELECT COALESCE("orders"."amount", "users"."id") AS "value" FROM "orders" WHERE ("orders"."user_id" = "users"."id")) AS "value" FROM "users"
}
```
source: [examples/query_correlated_projection_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/query_correlated_projection_example_test.go)
<!-- END INCLUDE -->

## Native SQL

Use `rasql.Native` when a statement depends on engine-specific syntax that the portable builder does not model. The
caller states the engine, SQL text, projection, and cardinality. Execution rejects an executor for another engine rather
than assuming native SQL is portable.

Generated static query functions use this same route. Their checked-in decoder and result schema keep native templates
on the normal terminal and codec path.

## Runtime-selected results

Use `rasql.DynamicProjection` when the result columns are known only at run time. It accepts a validated `ResultSchema`
and produces a projection whose rows retain ordered names and values. Combine it with `Select` or `Native`; there is no
separate dynamic builder or execution package.

## Reusable prepared queries

`rasql.Prepare` validates, lowers, renders and resolves codecs for a query once, and hands back a `Prepared[R]` whose
`Rows`, `All`, `One` and `Maybe` methods repeat none of that work across many runs. Placing a `rasql.Parameter` where a
`Value` would go lets one `Prepare` serve a different argument on each run: `Bind` returns a new `Prepared` carrying
the value, and the `Prepared` `Bind` was called on stays unbound, so it can be shared and bound differently by every
caller. Running a `Prepared` while a parameter has no value reports `parameter_unbound` before anything reaches the
database.

<!-- INCLUDE(examples/rasql_prepared_parameter_example_test.go#prepared_parameter) -->
```go
query, minTotal, err := preparedParamOrdersQuery()
if err != nil {
	fmt.Printf("failed to build orders query: %s\n", err)
	return
}
// Prepare validates, lowers, renders and resolves codecs once. minTotal
// still has no value, so running prepared as it stands would report
// parameter_unbound.
prepared, err := rasql.Prepare(executor, query)
if err != nil {
	fmt.Printf("failed to prepare query: %s\n", err)
	return
}

// Bind returns a new Prepared and leaves prepared itself unbound, so the
// same prepared form serves both minimum totals instead of preparing the
// query twice.
atLeast20, err := prepared.Bind(minTotal.Value(int64(20)))
if err != nil {
	fmt.Printf("failed to bind minimum total: %s\n", err)
	return
}
atLeast80, err := prepared.Bind(minTotal.Value(int64(80)))
if err != nil {
	fmt.Printf("failed to bind minimum total: %s\n", err)
	return
}
```
source: [examples/rasql_prepared_parameter_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_prepared_parameter_example_test.go)
<!-- END INCLUDE -->

## Choosing a terminal

| Terminal | Contract |
| --- | --- |
| `Rows` | Returns a lazy sequence and reports per-row decode errors. |
| `All` | Collects every row and returns an empty slice when none match. |
| `One` | Requires exactly one row. |
| `Maybe` | Accepts zero or one row and reports whether a row was present. |

The executor owns rendering, bind codecs, result codecs, row closure, event observation, and transaction durability.
Create one with `rasql.Open`, which returns a `DB` that already satisfies `Executor`.

## Next

[The SQL builder](core/02-sql-builder.md) covers dialect-neutral statements. [Typed queries](orm/03-typed-queries.md)
covers generated sources and projections. [Writing rows](orm/04-writing.md) covers mutation plans and batches.
