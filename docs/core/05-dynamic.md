# Dynamic rows

`rasql/dynamic` reads and writes rows for a column name the program only knows as a string when it runs. Every terminal on this page takes a `rasql.DB`, which carries the database handle and the dialect to render with, and none of them asks for a Go row type: a result arrives as a `dynamic.Row` and the caller reads the values it wants out of it.

Reach for it when the column names arrive as data rather than as source code. [Typed queries](../orm/03-typed-queries.md) covers the other side, where a generated table carries the row type and the builder decodes each row into it.

## Operation reference

Predicates, aggregates, and statement constructors are the same ones [the SQL builder](02-sql-builder.md#operation-reference) lists, because `dynamic` builds `query` statements too.

| Operation | Entry point | Result |
| --- | --- | --- |
| `SELECT` without decoding | `dynamic.SelectFrom(table.Ref())` | `dynamic.SelectBuilder`, yielding `dynamic.Row` |
| `SELECT` from a hand-built statement | `dynamic.Query(ctx, db, statement)` | `iter.Seq2[dynamic.Row, error]` |
| `DELETE` with no Go row type | `dynamic.DeleteFrom(table.Ref())` | `dynamic.DeleteBuilder` |
| `DELETE` with `RETURNING`, undecoded | `dynamic.DeleteFrom(table.Ref()).Returning(...)` | `dynamic.DeleteReturningBuilder`, yielding `dynamic.Row` |
| Write with `RETURNING`, undecoded | `query.New….WithReturning(...)` then `dynamic.QueryWrite(ctx, db, statement)` | `iter.Seq2[dynamic.Row, error]` |

## Select builder methods

`dynamic.SelectFrom` takes a `query.TableRef`, so a generated table joins in as `table.Ref()` and a hand-built `query.MustTableRef` works just as well. The builder has exactly one table and no generated column accessors, so it names its columns as plain strings.

| Method | Effect |
| --- | --- |
| `Select(names…)` | Adds primary-table columns by name. |
| `Project(projections…)` | Adds columns and function calls directly, and other expressions through `query.Project`. |
| `Distinct()` | De-duplicates result rows. |
| `Join(joins…)` | Adds a join built with `rasql.InnerJoin` or `rasql.LeftJoin`. |
| `Where(expression)` | Adds a predicate from a `query` expression. |
| `WhereEqual(name, value)` | Adds `column = value` for a primary-table column. |
| `WhereIn(name, values…)` | Adds `column IN (values…)` for a primary-table column, one placeholder per value. |
| `GroupBy(expressions…)` | Adds grouping built with the basic query API. |
| `GroupByColumns(names…)` | Adds primary-table columns to the grouping by name. |
| `Having(expression)` | Adds a grouped predicate from a `query` expression; combines with `AND` like `Where`. |
| `Order(orders…)` | Adds ordering built with `query.Asc` or `query.Desc`. |
| `OrderAsc(name)`, `OrderDesc(name)` | Adds ordering for a primary-table column. |
| `Limit(n)`, `Offset(n)` | Pages the result. |
| `Build(d)` | Renders `stmt.Statement` for a `dialect.Dialect` without executing. |
| `Query(ctx, db)` | Executes and returns a rangeable `iter.Seq2`; use it for a large result or an early stop. |
| `Count(ctx, db)` | Executes `COUNT(*)` over the matched rows in place of the builder's projections; rejects a builder with `Limit`, `Offset`, or `Distinct` set. |

`dynamic.SelectBuilder` has no `All` or `One`: it has no Go type to collect into, so a caller ranges its `Query` sequence directly or reads one row with `dynamic.Get`.

`Where`, `WhereEqual`, and `WhereIn` accumulate with `AND` here the same way they do on the typed builder, and `WhereIn` rejects an empty value list the same way. [Select builder methods](../orm/03-typed-queries.md#select-builder-methods) states both rules.

`Select` narrows the projection to named columns, which is what makes `Distinct()` meaningful: a builder that projects every column of a table, primary key included, has already made each row unique before `DISTINCT` runs. `GroupByColumns` is the same `names…` form for the grouping.

## Read a row

A `dynamic.Row` holds one result row with its column names. `dynamic.Scan` turns the `*sql.Rows` a `db.QueryRendered` call returns into the same rangeable sequence the builders yield, so a [static template](06-named-sql.md) reads its results the same way a builder does:

<!-- INCLUDE(examples/rasql_static_template_example_test.go#read_dynamic_rows) -->
```go
for result, err := range dynamic.Scan(sqlRows) {
	if err != nil {
		fmt.Printf("failed to query user: %s\n", err)
		return
	}
	email, err := dynamic.Get[string](result, "email")
	if err != nil {
		fmt.Printf("failed to read email: %s\n", err)
		return
	}
	fmt.Println(email)
}
```
source: [examples/rasql_static_template_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_static_template_example_test.go)
<!-- END INCLUDE -->

Read the values of a row in one of three ways:

| Call | Gives |
| --- | --- |
| `dynamic.Get[T](result, "email")` | One named value, decoded as `T`. |
| `dynamic.Assign(result, "email", &value)` | The same value, decoded into an existing destination. |
| `dynamic.Decode[T](result)` | A whole struct, matching `rasql` tags or snake-cased field names. |

A debug `Handle` may return `nil` rows after logging. `dynamic.Scan` reads that as an empty result rather than an error.

## Delete rows

`dynamic.DeleteFrom(table.Ref())` builds a delete for a table with no Go row type, and its `Returning(...)` reads the deleted rows back as `dynamic.Row` values from its own `Query`. [Write statements](03-write-statements.md#reading-a-returning-clause) covers the `RETURNING` clause itself, including which dialects have one.

## Next

[The database handle](04-database.md) runs the statements this builder produces, installs hooks, and starts a transaction. [Typed queries](../orm/03-typed-queries.md) decodes rows into a Go type instead.
