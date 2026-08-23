# Querying

`rasql` offers two builders for reading rows, one per layer, and this page says what each one is for. Both start from a table description, both validate the statement before it becomes SQL, and both send every value as a bound argument.

## Two builders

The [SQL builder](core/02-sql-builder.md) is the core layer, and it lives in `query` and `render`. `query` assembles a dialect-neutral statement and validates it, and `render` turns that statement into SQL text with its arguments in placeholder order. These two packages import `schema` and `dialect` and nothing else of `rasql`, so the statement is built with no database handle in hand and no Go type for the row. What comes back is a `stmt.Statement`, which holds the rendered SQL text and its arguments and nothing else, and which an application executes however it likes, including through `database/sql` directly.

The [typed builder](orm/03-typed-queries.md) is the ORM layer, and it lives in the root `rasql` package. It starts from a generated table value that carries the Go row type, so `rasql.SelectFrom(users)` already knows what a result row decodes into, and `users.ID()` is a column reference the compiler checks. It builds the same statements the SQL builder builds, then executes them and decodes each row into the row type.

| | SQL builder | Typed builder |
| --- | --- | --- |
| Packages | `query`, `render` | `rasql`, plus `rasql/dynamic` |
| Needs a generated table | No | Yes, or a hand-written one of the same shape |
| Needs a database handle | No | Yes, at the call that executes |
| Names a column as | `accounts.Column("id")`, checked against the descriptor at run time | `users.ID()`, checked by the compiler |
| Produces | `stmt.Statement`, holding SQL text and arguments | Decoded rows of the Go row type |

## Which one to reach for

| Situation | Builder |
| --- | --- |
| The application has a generated store and reads whole rows. | Typed builder. |
| A tool renders SQL for something else to run, or for a test to compare. | SQL builder. |
| The table is known only when the program runs. | SQL builder, executed through `rasql/dynamic`. |
| A join or a projection produces a shape that is not a table row. | Typed builder, through `rasql.DecodeFrom[R]`. |
| The statement uses syntax the builders do not model. | A [static template](core/06-named-sql.md), or `stmt.New` directly. |

The two are not exclusive. The typed builder takes `query` expressions in its `Where`, `Having`, `GroupBy`, and `Order` methods, so a predicate tree built with `query.And` and `query.Or` drops straight into a typed select. Its `Build(d)` method stops at the same `stmt.Statement` the SQL builder returns, which is how a typed query is inspected without a database.

## Where rasql/dynamic sits

`rasql/dynamic` opens a database and reads rows for a table that has no Go row type. It offers the same fluent shape as the typed builder, names its columns as strings, and yields `dynamic.Row` values instead of decoding. Use it when the column names arrive as data, and see [Dynamic rows](core/05-dynamic.md) for its methods. It belongs to the core layer, since a caller reaches it without generating anything.

## Next

[The SQL builder](core/02-sql-builder.md) covers the statement constructors, the expression constructors, and rendering. [Typed queries](orm/03-typed-queries.md) covers the typed builder and works through joins, grouping, subqueries, and custom result shapes. [Dynamic rows](core/05-dynamic.md) covers the builder that names its columns as strings. [Writing rows](orm/04-writing.md) covers inserts, updates, and deletes on both sides.
