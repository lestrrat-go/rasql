# Typed queries

`rasql` reads rows through a fluent builder. Start from `rasql.SelectFrom` when the result has a table's row type, and from `rasql.DecodeFrom` when a join or projection produces a shape of its own.

Columns come from the generated table value, so `users.ID()` is a `query.ColumnRef` already bound to the `users` table. A misspelled `users.Emial()` is a compile error rather than a failed query, which [What the column accessors catch](08-rasqlgen.md#what-the-column-accessors-catch) demonstrates along with the cases that still fail at run time.

Generated relationship methods provide a typed join and eager-loading path for the supported relationship slice described in [Relationships](02-schema.md#relationships). A child relation such as `orders.User()` exposes `Join()` for a fluent query and `Load(ctx, db, children)` for one batched lookup that returns related rows grouped by foreign-key value. The inverse parent relation such as `users.Orders()` returns children grouped by parent key. Use the ordinary `Join` API for unsupported relationship shapes.

Every builder is immutable. Each call returns a new builder, so a partly built query can be shared or reused without one caller's `Limit` leaking into another's.

A typed query is built from the same `query` expressions [The SQL builder](04-sql-builder.md) describes and renders through the same `render` package. What the root package adds is the row type: the table value knows it, so the builder decodes each result row without being told the shape a second time.

## Operation reference

The tables in this section enumerate every operation the typed API offers. The sections after them show the common ones in use. Predicates, aggregates, and statement constructors live in [the SQL builder reference](04-sql-builder.md#operation-reference).

### Statements

The first table is the typed layer in `rasql`. The second is `rasql/dynamic`, for a column name you only know as a string when the program runs.

| Operation | Entry point | Result |
| --- | --- | --- |
| `SELECT` decoded as a table's row type | `rasql.SelectFrom(table)` | `TypedSelectBuilder[T]` |
| `SELECT` decoded as a custom type | `rasql.DecodeFrom[R](table)` | `TypedSelectBuilder[R]` |
| `SELECT` decoded from a table with no row type | `rasql.DecodeFromRef[R](queryTable)` | `TypedSelectBuilder[R]` |
| Typed static SQL | `rasql.QueryRendered[T](ctx, db, statement)` | `iter.Seq2[T, error]` |
| `INSERT` of one typed row | `rasql.Insert(ctx, db, table, value)` | `sql.Result` |
| `INSERT` with database defaults | `rasql.InsertWithOptions(ctx, db, table, value, rasql.DefaultColumns(...))` | `sql.Result` |
| `INSERT` of several typed rows | `rasql.InsertMany(ctx, db, table, values)` | `sql.Result` |
| `INSERT` of several typed rows with defaults | `rasql.InsertManyWithOptions(ctx, db, table, values, rasql.DefaultColumns(...))` | `sql.Result` |
| `INSERT` of expression rows | `query.NewInsertRows(table.Ref(), columns, rows)` then `rasql.Exec(ctx, db, statement)` | `sql.Result` |
| `UPDATE` of one typed row by primary key | `rasql.Update(ctx, db, table, value)` | `sql.Result` |
| `UPDATE` selected typed fields | `rasql.UpdateWithOptions(ctx, db, table, value, rasql.UpdateColumns(...))` | `sql.Result` |
| `UPDATE` many rows by predicate | `rasql.UpdateMany(ctx, db, table, value, rasql.UpdateColumns(...), rasql.UpdateWhere(...))` | `sql.Result` |
| `UPDATE` with arbitrary expressions | `query.NewUpdate(table.Ref(), assignments…)` then `rasql.Exec(ctx, db, statement)` | `sql.Result` |
| `DELETE` by predicate | `rasql.DeleteFrom(table)` | `DeleteBuilder` |
| `DELETE` with `RETURNING` | `rasql.DeleteFrom(table).Returning(...)` | `DeleteReturningBuilder` |
| `CREATE TABLE` plus its indexes | `rasql.CreateTable(ctx, db, table)` | `error` |
| Upsert | `query.New…` then `rasql.Exec(ctx, db, statement)` | `sql.Result` |
| Write with `RETURNING`, decoded | `query.New….WithReturning(...)` then `rasql.QueryWriteAll[T]` / `rasql.QueryWriteOne[T]` | `[]T` / `T` |
| Compiled [static template](07-templates.md) | `db.ExecRendered(ctx, statement)` | `sql.Result` |

#### rasql/dynamic

| Operation | Entry point | Result |
| --- | --- | --- |
| `SELECT` without decoding | `dynamic.SelectFrom(table.Ref())` | `dynamic.SelectBuilder`, yielding `dynamic.Row` |
| `SELECT` from a hand-built statement | `dynamic.Query(ctx, db, statement)` | `iter.Seq2[dynamic.Row, error]` |
| `DELETE` with no Go row type | `dynamic.DeleteFrom(table.Ref())` | `dynamic.DeleteBuilder` |
| `DELETE` with `RETURNING`, undecoded | `dynamic.DeleteFrom(table.Ref()).Returning(...)` | `dynamic.DeleteReturningBuilder`, yielding `dynamic.Row` |
| Write with `RETURNING`, undecoded | `query.New….WithReturning(...)` then `dynamic.QueryWrite(ctx, db, statement)` | `iter.Seq2[dynamic.Row, error]` |

Writes are covered in [Writing rows](06-writing.md); the rest of this page covers reads.

### Select builder methods

The typed builder comes from `SelectFrom`, `DecodeFrom`, and `DecodeFromRef` in `rasql`. The builder below it comes from `dynamic.SelectFrom`.

The two builders differ in how they name a column. `TypedSelectBuilder` takes a `query.ColumnRef`, usually a generated accessor such as `users.ID()`, so a wrong name does not compile and a join can order by a column of any table in the statement. `dynamic.SelectBuilder` has exactly one table and no generated columns, so it keeps plain names.

`TypedSelectBuilder`:

| Method | Effect |
| --- | --- |
| `Project(projections…)` | Adds projections built with `query.Project`. |
| `Distinct()` | De-duplicates result rows. |
| `Join(joins…)` | Adds a join built with `rasql.InnerJoin` or `rasql.LeftJoin`. |
| `Where(expression)` | Adds a predicate from a `query` expression. |
| `WhereEqual(column, value)` | Adds `column = value` for a `query.ColumnRef`. |
| `WhereIn(column, values…)` | Adds `column IN (values…)` for a `query.ColumnRef`, one placeholder per value. |
| `GroupBy(expressions…)` | Adds grouping built with the basic query API. |
| `Having(expression)` | Adds a grouped predicate from a `query` expression; combines with `AND` like `Where`. |
| `Order(orders…)` | Adds ordering built with `query.Asc` or `query.Desc`. |
| `OrderAsc(column)`, `OrderDesc(column)` | Adds ordering for a `query.ColumnRef`. |
| `Limit(n)`, `Offset(n)` | Pages the result. |
| `Build(d)` | Renders `render.Statement` for a `dialect.Dialect` without executing. |
| `Query(ctx, db)` | Executes and returns a rangeable `iter.Seq2`; use it for a large result or an early stop. |
| `All(ctx, db)` | Executes and collects `[]T`; use it when the whole result fits in memory. |
| `One(ctx, db)` | Executes and returns one `T`; returns `rasql.ErrNoRows` for zero rows or `rasql.ErrMultipleRows` for more than one. |
| `Count(ctx, db)` | Executes `COUNT(*)` over the matched rows in place of the builder's projections; rejects a builder with `Limit`, `Offset`, or `Distinct` set. |

`dynamic.SelectBuilder`:

| Method | Effect |
| --- | --- |
| `Select(names…)` | Adds primary-table columns by name. |
| `Project(projections…)` | Adds projections built with `query.Project`. |
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
| `Build(d)` | Renders `render.Statement` for a `dialect.Dialect` without executing. |
| `Query(ctx, db)` | Executes and returns a rangeable `iter.Seq2`; use it for a large result or an early stop. |
| `Count(ctx, db)` | Executes `COUNT(*)` over the matched rows in place of the builder's projections; rejects a builder with `Limit`, `Offset`, or `Distinct` set. |

`dynamic.SelectBuilder` has no `All` or `One`: it has no Go type to collect into, so a caller ranges its `Query` sequence directly or reads one row with `dynamic.Get`.

`Where`, `WhereEqual`, and `WhereIn` accumulate: repeated calls combine with
`AND` in the order they were made, which is what a conditionally built filter
needs. Use a single `query.Or` call for a top-level `OR`; it is not wrapped in
an `AND` unless another `Where`, `WhereEqual`, or `WhereIn` follows it.
`WhereIn` needs at least one value on either builder: an empty list makes
`Build` and the executing methods return an error rather than render `IN ()`,
which is not valid SQL in any supported dialect.

### Delete builder methods

| Method | Effect |
| --- | --- |
| `Where(expression)` | Adds a predicate from a `query` expression. |
| `WhereEqual(column, value)` | Adds `column = value` for a `query.ColumnRef` of the target table. |
| `WhereIn(column, values…)` | Adds `column IN (values…)` for a `query.ColumnRef` of the target table, one placeholder per value. |
| `Returning(projections…)` | Adds a `RETURNING` clause and returns `DeleteReturningBuilder`; MySQL does not support it. |
| `Build(d)` | Renders `render.Statement` for a `dialect.Dialect` without executing. |
| `Exec(ctx, db)` | Executes and returns `sql.Result`. |

Pass the builder to `rasql.QueryDeleteAll[T]` or `rasql.QueryDeleteOne[T]` to
decode typed rows. `rasql.DeleteFrom` has no undecoded terminal:
`dynamic.DeleteFrom(table.Ref()).Returning(...).Query(ctx, db)` reads the same
rows as `dynamic.Row` values.

`Where`, `WhereEqual`, and `WhereIn` accumulate on the delete builder the same way: repeated calls combine with `AND` in the order they were made. Each of them supplies the predicate that `Build` and `Exec` require, so a delete still needs one of them or an explicit `AllowAll`. `WhereIn` needs at least one value: an empty list makes `Build` and `Exec` return an error rather than render `IN ()`, which is not valid SQL in any supported dialect.

## Select typed rows

`SelectFrom` knows the row type from the table descriptor, so it selects every column and decodes each result into that type.

<!-- INCLUDE(examples/rasql_typed_query_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_typed_query() {
	// This example pages through several users and decodes them as UserRow values.
	// users and UserRow are declared in query_example_tables_test.go with the
	// shape rasqlgen emits; an application that generated into package store
	// would write store.Users() and store.UsersRow instead.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A DB couples a database handle with the dialect used to render SQL.
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	// Create the table described by the generated users descriptor.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	// Use rasql.Insert for each fixture row so setup follows the public API.
	for _, user := range []UserRow{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@example.com"},
	} {
		if _, err := rasql.Insert(ctx, db, users, user); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// SelectFrom knows the UsersRow result type from users. Query yields decoded
	// rows directly, so the loop does not need manual scanning or conversion.
	// SQL: SELECT users.id, users.email FROM users ORDER BY users.email ASC LIMIT 2 OFFSET 1
	rows, err := rasql.SelectFrom(users).
		OrderAsc(users.Email()).
		Offset(1).
		Limit(2).
		Query(ctx, db)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	for found, err := range rows {
		if err != nil {
			fmt.Printf("failed to query users: %s\n", err)
			return
		}
		fmt.Println(found.Email)
	}

	// Output:
	// bob@example.com
	// cyd@example.com
}
```
source: [examples/rasql_typed_query_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_typed_query_example_test.go)
<!-- END INCLUDE -->

`Query`, `All`, and `One` run the statement, as listed under [Select builder methods](#select-builder-methods). All three report validation and rendering problems as the returned error before any row is read. `Query` yields rows first and at most one error after them, so a loop checks the error on each step and stops when it is non-nil.

`One` also reports the result's row count: it returns `rasql.ErrNoRows` when the statement matched no rows and `rasql.ErrMultipleRows` when it matched more than one. `rasql.ErrNoRows` wraps `sql.ErrNoRows`, so `errors.Is(err, sql.ErrNoRows)` holds too, and code already written against `database/sql` keeps working:

<!-- INCLUDE(examples/rasql_no_rows_example_test.go#no_rows) -->
```go
_, err = rasql.SelectFrom(users).WhereEqual(users.ID(), 1).One(ctx, db)
if errors.Is(err, rasql.ErrNoRows) {
	fmt.Println("no such user")
}
```
source: [examples/rasql_no_rows_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_no_rows_example_test.go)
<!-- END INCLUDE -->

`Build(d)` skips execution and returns the rendered `render.Statement`, which carries the SQL text and its ordered arguments. It takes a `dialect.Dialect` rather than a `rasql.DB`, because rendering needs the dialect and nothing else. It is the direct way to log or test a statement.

## Filter, order, and page

`WhereEqual`, `OrderAsc`, and `OrderDesc` take a `query.ColumnRef` and cover the common cases without importing the `query` package. Generated tables expose one accessor method per column, so `users.ID()` is the whole reference. `Limit` and `Offset` page the result. `dynamic.SelectBuilder` from `dynamic.SelectFrom` also has `Select`, which narrows the projection to named columns. A caller who wants the raw row and full manual control drops to `dynamic.SelectFrom(table.Ref()).Query(ctx, db)` instead.

`WhereIn` covers a membership test the same way. It needs at least one value: an empty list makes `Build`, `Query`, `All`, and `One` return an error rather than render `IN ()`, which is not valid SQL in any supported dialect. A non-empty list binds each value as its own placeholder:

<!-- INCLUDE(examples/rasql_where_in_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_where_in() {
	// This example selects rows whose id is one of a fixed set of values.
	// users and UserRow are declared in query_example_tables_test.go with the
	// shape rasqlgen emits; an application that generated into package store
	// would write store.Users() and store.UsersRow instead.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A DB couples a database handle with the dialect used to render SQL.
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	// Create the table described by the generated users descriptor.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	for _, user := range []UserRow{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@example.com"},
	} {
		if _, err := rasql.Insert(ctx, db, users, user); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// WhereIn binds one placeholder per value and skips the users whose id is
	// not in the list. The list must hold at least one value: an empty one makes
	// Query return an error instead of rendering IN (), which is not valid SQL.
	// SQL: SELECT users.id, users.email FROM users WHERE users.id IN (?, ?) ORDER BY users.id ASC (arguments: 1, 3)
	rows, err := rasql.SelectFrom(users).
		WhereIn(users.ID(), 1, 3).
		OrderAsc(users.ID()).
		Query(ctx, db)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	for found, err := range rows {
		if err != nil {
			fmt.Printf("failed to query users: %s\n", err)
			return
		}
		fmt.Println(found.Email)
	}

	// Output:
	// ada@example.com
	// cyd@example.com
}
```
source: [examples/rasql_where_in_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_where_in_example_test.go)
<!-- END INCLUDE -->

For anything richer, `Where` and `Order` accept expressions from the `query` package:

<!-- INCLUDE(examples/rasql_where_expressions_example_test.go#where_expressions) -->
```go
rows, err := rasql.SelectFrom(users).
	Where(query.And(
		query.GreaterThan(users.ID(), query.Bind(10)),
		query.IsNotNull(users.ID()),
	)).
	Order(query.Desc(users.ID())).
	Query(ctx, db)
```
source: [examples/rasql_where_expressions_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_where_expressions_example_test.go)
<!-- END INCLUDE -->

A generated accessor cannot name a column the table does not have, because the method would not exist. A table built at run time has no such accessors, so `table.Column(name)` looks the column up in the descriptor and fails when the table has no such column; a typo surfaces while the query is being assembled rather than as a database error later. `query.Bind` marks a value as an argument; the renderer turns it into the dialect's placeholder and appends it to the argument list. No public API puts a value into SQL text.

## Nest a predicate tree

`query.And` and `query.Or` take expressions and return one, so either holds the other and a filter that mixes them is a single tree passed to a single `Where` call. Nothing limits the depth, and every constructor in [Where conditions](04-sql-builder.md#where-conditions) can sit at any node.

<!-- INCLUDE(examples/rasql_nested_predicates_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// Example_rasql_nested_predicates builds a predicate tree several levels deep
// and shows the SQL it renders, which is what a filter that mixes AND and OR
// needs. query.And and query.Or take expressions and return one, so either
// holds the other to any depth.
func Example_rasql_nested_predicates() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	for _, user := range []UserRow{
		{ID: 5, Email: "ada@example.com"},
		{ID: 7, Email: "linus@other.org"},
		{ID: 15, Email: "grace@example.com"},
		{ID: 25, Email: "alan@example.com"},
	} {
		if _, err := rasql.Insert(ctx, db, users, user); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// The inner query.And sits inside a query.Or, which sits inside the Where
	// call, and the whole tree is one predicate. The builder is immutable, so
	// the same value below renders the statement and then runs it.
	selected := rasql.SelectFrom(users).
		Where(query.Like(users.Email(), query.Bind("%@example.com"))).
		Where(query.Or(
			query.LessThan(users.ID(), query.Bind(10)),
			query.And(
				query.GreaterThan(users.ID(), query.Bind(20)),
				query.IsNotNull(users.Email()),
			),
		)).
		Order(query.Asc(users.ID()))

	// Every level of the tree renders its own parentheses, so the SQL groups the
	// way the Go code nests rather than by the database's operator precedence.
	statement, err := selected.Build(db.Dialect())
	if err != nil {
		fmt.Printf("failed to build statement: %s\n", err)
		return
	}
	fmt.Println(statement.SQL())

	found, err := selected.All(ctx, db)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	for _, user := range found {
		fmt.Println(user.ID, user.Email)
	}

	// Output:
	// SELECT "users"."id", "users"."email" FROM "users" WHERE (("users"."email" LIKE ?) AND (("users"."id" < ?) OR (("users"."id" > ?) AND ("users"."email" IS NOT NULL)))) ORDER BY "users"."id"
	// 5 ada@example.com
	// 25 alan@example.com
}
```
source: [examples/rasql_nested_predicates_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_nested_predicates_example_test.go)
<!-- END INCLUDE -->

Each level renders its own parentheses, which the `WHERE` clause printed above shows: the SQL groups the way the Go code nests, so no filter depends on the database's operator precedence.

The two `Where` calls above combine with `AND` under the rule that [Select builder methods](#select-builder-methods) states, and the tree the second one carries becomes one operand of that `AND`. Build the tree instead when a group has to bind tighter than the accumulating `AND`: `Where(a).Where(query.Or(b, c))` matches `a AND (b OR c)`, while `Where(a).Where(b).Where(c)` matches `a AND b AND c`.

## Filter with a subquery

`query.InSelect` and `query.Scalar` take a `SELECT` statement as the right-hand side of a predicate, in place of a value list or a bound value. Each subquery is validated and rendered as its own statement; [Subqueries](04-sql-builder.md#subqueries) covers the placement rules, including MySQL's restriction on `LIMIT` inside an `InSelect` statement.

<!-- INCLUDE(examples/rasql_subquery_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_subquery() {
	// This example selects orders placed by a user reachable by email domain,
	// then narrows to orders at or above the average amount across every order.
	// users and UserRow are declared in query_example_tables_test.go with the
	// shape rasqlgen emits; an application that generated into package store
	// would write store.Users() and store.UsersRow instead.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A DB couples a database handle with the dialect used to render SQL.
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	// A typed descriptor makes orders usable with rasql.Insert as well.
	type orderRow struct {
		ID     int `rasql:"id"`
		UserID int `rasql:"user_id"`
		Amount int `rasql:"amount"`
	}
	// A local result type projects only orders columns, so no join is needed:
	// both subqueries below run as their own SELECT, never as part of this one.
	type orderSummary struct {
		UserID int64
		Amount int64
	}
	orders := rasql.MustTableOf[orderRow](schema.MustTableDef("orders",
		schema.Integer("id"),
		schema.Integer("user_id"),
		schema.Integer("amount"),
		schema.PrimaryKey("id"),
	))
	// Create both descriptors before querying orders against the users subquery.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if err := rasql.CreateTable(ctx, db, orders); err != nil {
		fmt.Printf("failed to create orders table: %s\n", err)
		return
	}

	// orders has no generated column accessors, so its columns are looked up by name.
	// That lookup validates them against the descriptor as the query is assembled.
	orderUserID, err := orders.Column("user_id")
	if err != nil {
		fmt.Printf("failed to find orders.user_id: %s\n", err)
		return
	}
	amount, err := orders.Column("amount")
	if err != nil {
		fmt.Printf("failed to find orders.amount: %s\n", err)
		return
	}

	for _, user := range []UserRow{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@other.example"},
	} {
		if _, err := rasql.Insert(ctx, db, users, user); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}
	for _, order := range []orderRow{
		{ID: 1, UserID: 1, Amount: 80},
		{ID: 2, UserID: 2, Amount: 20},
		{ID: 3, UserID: 3, Amount: 100},
	} {
		if _, err := rasql.Insert(ctx, db, orders, order); err != nil {
			fmt.Printf("failed to insert order: %s\n", err)
			return
		}
	}

	// domainUsers selects the id of every user whose email ends in the chosen
	// domain. It reads no table of the enclosing statement, so it validates and
	// renders as its own SELECT.
	domainUsers, err := query.NewSelect(users.Ref(), query.Project(users.ID()))
	if err != nil {
		fmt.Printf("failed to build domain-users subquery: %s\n", err)
		return
	}
	domainUsers, err = domainUsers.WithWhere(query.Like(users.Email(), query.Bind("%@example.com")))
	if err != nil {
		fmt.Printf("failed to filter domain-users subquery: %s\n", err)
		return
	}

	// allOrders aliases orders so the average subquery is a separate scope from
	// the orders read by the enclosing statement, even though it names the same
	// table.
	allOrders, err := rasql.As(orders, "all_orders")
	if err != nil {
		fmt.Printf("failed to alias orders: %s\n", err)
		return
	}
	allOrdersAmount, err := allOrders.Column("amount")
	if err != nil {
		fmt.Printf("failed to find all_orders.amount: %s\n", err)
		return
	}
	average, err := query.NewSelect(allOrders.Ref(), query.Project(query.Avg(allOrdersAmount)))
	if err != nil {
		fmt.Printf("failed to build average subquery: %s\n", err)
		return
	}

	// InSelect keeps orders placed by a domain user without costing one
	// argument per candidate id, and Scalar compares amount against the
	// average of every order.
	// SQL: SELECT orders.user_id, orders.amount FROM orders WHERE orders.user_id IN (SELECT users.id FROM users WHERE users.email LIKE ?) AND orders.amount >= (SELECT AVG(all_orders.amount) FROM orders AS all_orders) ORDER BY orders.amount ASC (argument: "%@example.com")
	rows, err := rasql.DecodeFrom[orderSummary](orders).
		Project(query.Project(orderUserID).As("user_id"), query.Project(amount)).
		Where(query.InSelect(orderUserID, domainUsers)).
		Where(query.GreaterThanOrEqual(amount, query.Scalar(average))).
		Order(query.Asc(amount)).
		Query(ctx, db)
	if err != nil {
		fmt.Printf("failed to query orders: %s\n", err)
		return
	}
	for summary, err := range rows {
		if err != nil {
			fmt.Printf("failed to query orders: %s\n", err)
			return
		}
		fmt.Println(summary.UserID, summary.Amount)
	}

	// Output:
	// 1 80
}
```
source: [examples/rasql_subquery_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_subquery_example_test.go)
<!-- END INCLUDE -->

## Count rows

`Count` runs `COUNT(*)` over the builder's joins and every predicate it accumulated, in place of its projections, so it counts exactly the rows the same builder would return. That is the common need for a paginated list: get the total once, unpaged, then page a separate copy of the builder for the rows.

<!-- INCLUDE(examples/rasql_count_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_count() {
	// This example counts rows matched by a builder without paging through them.
	// users and UserRow are declared in query_example_tables_test.go with the
	// shape rasqlgen emits; an application that generated into package store
	// would write store.Users() and store.UsersRow instead.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A DB couples a database handle with the dialect used to render SQL.
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	// Create the table described by the generated users descriptor.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	// Use rasql.Insert for each fixture row so setup follows the public API.
	for _, user := range []UserRow{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@example.com"},
	} {
		if _, err := rasql.Insert(ctx, db, users, user); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// Count runs COUNT(*) over the builder's WHERE and joins, without decoding
	// any row into a UserRow. It rejects a builder with Limit or Offset set,
	// since a count of a paged statement is not the count the caller asked for.
	// SQL: SELECT COUNT(*) FROM users
	total, err := rasql.SelectFrom(users).Count(ctx, db)
	if err != nil {
		fmt.Printf("failed to count users: %s\n", err)
		return
	}
	fmt.Println("total:", total)

	// SQL: SELECT COUNT(*) FROM users WHERE users.id = ? (argument: 2)
	filtered, err := rasql.SelectFrom(users).WhereEqual(users.ID(), 2).Count(ctx, db)
	if err != nil {
		fmt.Printf("failed to count filtered users: %s\n", err)
		return
	}
	fmt.Println("filtered:", filtered)

	// Output:
	// total: 3
	// filtered: 1
}
```
source: [examples/rasql_count_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_count_example_test.go)
<!-- END INCLUDE -->

`Count` rejects a builder that sets `Limit` or `Offset`, because a count of a paged statement is not the count the caller built the statement to ask for; count an unpaged builder, then page a copy of it for the rows. `SUM` and `AVG` have no equivalent helper, because their result types are not portable across dialects — project them with `query.Sum` or `query.Avg` and decode through `rasql.DecodeFrom[R]` instead, as [Aggregates](04-sql-builder.md#aggregates) covers.

## Group rows

`GroupBy` adds a `GROUP BY` clause, which is what lets a projection set mix a bare column with an aggregate: [Aggregates](04-sql-builder.md#aggregates) refuses that combination without one. `Having` adds a `HAVING` clause, filtering groups after aggregation the way `Where` filters rows before it; repeated calls combine with `AND` in the order they were made, exactly as `Where` does. `Having` needs groups to filter, so it requires either a `GroupBy` or a projection set that aggregates and reads no column outside an aggregate, a set in which a projection reading no column — a bound value, say — may sit beside the aggregate. [Aggregates](04-sql-builder.md#aggregates) states what each of those two cases allows the clause to read.

<!-- INCLUDE(examples/rasql_group_by_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_group_by() {
	// This example counts tasks per status and keeps only the statuses with
	// more than one task, using GROUP BY and HAVING together.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A DB couples a database handle with the dialect used to render SQL.
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}

	// A typed descriptor makes tasks usable with rasql.Insert.
	type taskRow struct {
		ID     int    `rasql:"id"`
		Status string `rasql:"status"`
	}
	// A local result type holds one row per group.
	type statusCount struct {
		Status string
		Total  int64
	}
	tasks := rasql.MustTableOf[taskRow](schema.MustTableDef("tasks",
		schema.Integer("id"),
		schema.Text("status"),
		schema.PrimaryKey("id"),
	))
	if err := rasql.CreateTable(ctx, db, tasks); err != nil {
		fmt.Printf("failed to create tasks table: %s\n", err)
		return
	}
	for _, task := range []taskRow{
		{ID: 1, Status: "open"},
		{ID: 2, Status: "open"},
		{ID: 3, Status: "done"},
		{ID: 4, Status: "done"},
		{ID: 5, Status: "done"},
	} {
		if _, err := rasql.Insert(ctx, db, tasks, task); err != nil {
			fmt.Printf("failed to insert task: %s\n", err)
			return
		}
	}

	// tasks has no generated column accessor for status, so it is looked up
	// by name. That lookup validates it against the descriptor as the query is
	// assembled.
	status, err := tasks.Column("status")
	if err != nil {
		fmt.Printf("failed to find tasks.status: %s\n", err)
		return
	}

	// GroupBy adds the GROUP BY clause the mixed projection below needs: a
	// bare column beside COUNT(*) is refused without one. Having filters
	// groups after aggregation, so it may call an aggregate a WHERE clause
	// could not.
	// SQL: SELECT tasks.status, COUNT(*) AS total FROM tasks GROUP BY tasks.status HAVING COUNT(*) > ? ORDER BY tasks.status (argument: 1)
	rows, err := rasql.DecodeFrom[statusCount](tasks).
		Project(query.Project(status), query.Project(query.CountAll()).As("total")).
		GroupBy(status).
		Having(query.GreaterThan(query.CountAll(), query.Bind(1))).
		Order(query.Asc(status)).
		Query(ctx, db)
	if err != nil {
		fmt.Printf("failed to query status counts: %s\n", err)
		return
	}
	for found, err := range rows {
		if err != nil {
			fmt.Printf("failed to query status counts: %s\n", err)
			return
		}
		fmt.Println(found.Status, found.Total)
	}

	// Output:
	// done 3
	// open 2
}
```
source: [examples/rasql_group_by_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_group_by_example_test.go)
<!-- END INCLUDE -->

`dynamic.SelectBuilder` groups by a primary-table column name with `GroupByColumns`, the counterpart to `Select`'s `names…` form, without importing the `query` package.

## Select distinct rows

`Distinct()` adds `DISTINCT` right after `SELECT`, so the statement returns one row per distinct combination of its projected values. It is meaningful mainly beside a narrowed projection: `rasql.SelectFrom[T]` already selects every column of the table, including its primary key, which makes every row unique before `DISTINCT` runs. Use `rasql.DecodeFrom[R]` with `Project`, or `dynamic.SelectBuilder`'s `Select` with specific column names, to narrow the projection first.

`Distinct` composes with everything else the builder offers — joins, `Where`, `GroupBy` and `Having`, `Order`, and `Limit`/`Offset` — with one rule left to the database rather than enforced in Go: an `ORDER BY` expression that is not among the distinct projections. SQLite accepts that shape and answers it from whichever row survived de-duplication, which is no question the caller asked; PostgreSQL refuses it with SQLSTATE `42P10`, and MySQL refuses it with error 3065 `ER_FIELD_IN_ORDER_NOT_SELECT`. rasql renders what the caller asks for and lets the database report that error, the way it already does for a WHERE clause that outgrows a dialect's parameter limit, rather than reimplementing the two servers' rule in Go.

<!-- INCLUDE(examples/rasql_distinct_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_distinct() {
	// This example lists the users who have placed at least one order,
	// without repeating a user who placed more than one.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A DB couples a database handle with the dialect used to render SQL.
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}

	// A typed descriptor makes orders usable with rasql.Insert.
	type orderRow struct {
		ID     int `rasql:"id"`
		UserID int `rasql:"user_id"`
	}
	// A local result type holds one row per distinct user id.
	type orderingUser struct {
		UserID int64 `rasql:"user_id"`
	}
	orders := rasql.MustTableOf[orderRow](schema.MustTableDef("orders",
		schema.Integer("id"),
		schema.Integer("user_id"),
		schema.PrimaryKey("id"),
	))
	if err := rasql.CreateTable(ctx, db, orders); err != nil {
		fmt.Printf("failed to create orders table: %s\n", err)
		return
	}
	for _, order := range []orderRow{
		{ID: 1, UserID: 1},
		{ID: 2, UserID: 2},
		{ID: 3, UserID: 1},
	} {
		if _, err := rasql.Insert(ctx, db, orders, order); err != nil {
			fmt.Printf("failed to insert order: %s\n", err)
			return
		}
	}

	// orders has no generated column accessor for user_id, so it is looked up
	// by name. That lookup validates it against the descriptor as the query is
	// assembled.
	orderUserID, err := orders.Column("user_id")
	if err != nil {
		fmt.Printf("failed to find orders.user_id: %s\n", err)
		return
	}

	// Distinct is meaningful here because Project narrows the result to
	// user_id alone; SelectFrom would already select the orders primary key,
	// which makes every row unique before DISTINCT runs.
	// SQL: SELECT DISTINCT orders.user_id FROM orders ORDER BY orders.user_id
	rows, err := rasql.DecodeFrom[orderingUser](orders).
		Project(query.Project(orderUserID).As("user_id")).
		Distinct().
		Order(query.Asc(orderUserID)).
		Query(ctx, db)
	if err != nil {
		fmt.Printf("failed to query ordering users: %s\n", err)
		return
	}
	for found, err := range rows {
		if err != nil {
			fmt.Printf("failed to query ordering users: %s\n", err)
			return
		}
		fmt.Println(found.UserID)
	}

	// Output:
	// 1
	// 2
}
```
source: [examples/rasql_distinct_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_distinct_example_test.go)
<!-- END INCLUDE -->

`Count` rejects a distinct builder, because it replaces the projections with `COUNT(*)`: `SELECT DISTINCT COUNT(*)` is always exactly one row, never the number of distinct rows. `query.Count(column).WithDistinct()`, which [Aggregates](04-sql-builder.md#aggregates) covers, counts the distinct non-NULL values of one column and decodes through `rasql.DecodeFrom[R]`. It is not a count of the rows `Distinct()` returns: it ignores NULL, which `DISTINCT` keeps as a value, and it takes only one expression rather than the several a distinct row de-duplicates on. The derived table or CTE that a portable distinct-row count needs is unsupported.

`DISTINCT ON`, PostgreSQL's syntax for keeping one row per group by explicit ordering, is out of scope: it needs its own dialect capability, since PostgreSQL is the only supported database that has it.

## Alias a table for a self-join

`As` returns the table under an alias. The column accessors read the table value they are called on, so the alias qualifies the columns reached through the aliased value with nothing to rebind:

<!-- INCLUDE(examples/rasql_self_join_example_test.go#self_join) -->
```go
manager, err := employees.As("manager")
if err != nil {
	fmt.Printf("failed to alias employees: %s\n", err)
	return
}
rows, err := rasql.SelectFrom(employees).
	Join(rasql.InnerJoin(manager, query.Equal(employees.ManagerID(), manager.ID()))).
	OrderAsc(employees.ID()).
	Query(ctx, db)
```
source: [examples/rasql_self_join_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_self_join_example_test.go)
<!-- END INCLUDE -->

`employees.ID()` still renders as `"employees"."id"`, while `manager.ID()` renders as `"manager"."id"`. `As` fails when the alias is not a valid identifier.

A table whose descriptor names a `Schema` (see [Qualify a table with a schema](02-schema.md#qualify-a-table-with-a-schema)) renders `"schema"."table"` in `FROM` and every write statement's target, and a column reached through it renders `"schema"."table"."column"`. An alias replaces a qualified table's whole name: once `events.As("e")` is taken, `e.ID()` renders as `"e"."id"`, not `"audit"."e"."id"`, and this holds for every alias regardless of whether the aliased table was qualified.

### Every source of one statement needs its own name

A statement may carry two sources only when a server can tell their column references apart, and `Validate` refuses the statement when it cannot. A refused statement reports `table "…" is referred to as "…", which already refers to table "…"`, naming the two tables it could not separate. A source is referred to by its alias when it has one, and by its table name otherwise. Two sources therefore clash whenever:

- they share an alias, whatever the two tables are and whichever schemas they come from;
- one carries an alias that repeats another source's unaliased table name;
- they share a table name and at least one of them is unqualified, because a bare `"users"."id"` names an unqualified `users` and a qualified `tenant_a.users` equally.

Two unaliased tables of the same name in *different* schemas do not clash. Each renders its columns under its own `"schema"."table"` prefix, so `tenant_a.users` joined to `tenant_b.users` renders a statement whose every column reference names exactly one source. `TestSQLiteRefusesAmbiguousSources` runs that join against SQLite alongside each shape validation refuses.

rasql refuses a clash rather than inventing an alias to separate the two sources. An alias it chose would change the SQL you asked for and, through a projected column's result name, what a decoded row looks like, and it cannot tell which of the two sources a column reference you already wrote was meant to reach. Repair a refused statement by calling `As` on one of the two sources.

PostgreSQL and MySQL refuse the same shape unconditionally on their own, PostgreSQL with SQLSTATE 42712 `duplicate_alias` and MySQL with error 1066 `ER_NONUNIQ_TABLE`. SQLite is the exception. It accepts two sources under one name and fails only on a column reference it cannot resolve to exactly one of them, so `users AS u INNER JOIN orders AS u` runs on SQLite alone, and rasql refuses it on every dialect.

[Where conditions](04-sql-builder.md#where-conditions) lists every comparison, logical connective, and null test the expression set offers.

## Decode a custom shape

A join or a narrowed projection does not return a table's row type. `DecodeFrom` names the result type instead, and maps each selected column onto its fields, matching a `rasql` tag if present and the snake-cased field name otherwise. A field no single column holds is computed by a method on the result type from the raw columns beside it, or converted by a field type implementing `sql.Scanner`; see [the mapping methods](08-rasqlgen.md#the-mapping-and-scan-methods). Use `DecodeFromRef` when the primary table is a bare `query.TableRef` with no Go row type.

<!-- INCLUDE(examples/rasql_dynamic_projection_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_dynamic_projection() {
	// This example joins users and orders, then reads an ad hoc result shape.
	// users and UserRow are declared in query_example_tables_test.go with the
	// shape rasqlgen emits; an application that generated into package store
	// would write store.Users() and store.UsersRow instead.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A DB couples a database handle with the dialect used to render SQL.
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	// A typed descriptor makes orders usable with rasql.Insert as well.
	type orderRow struct {
		ID     int `rasql:"id"`
		UserID int `rasql:"user_id"`
		Total  int `rasql:"total"`
	}
	// A local result type makes the custom projection as easy to read as a table row.
	type orderSummary struct {
		UserID int64
		Email  string
	}
	orders := rasql.MustTableOf[orderRow](schema.MustTableDef("orders",
		schema.Integer("id"),
		schema.Integer("user_id"),
		schema.Integer("total"),
		schema.PrimaryKey("id"),
	))
	// Create both descriptors before querying their joined rows.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if err := rasql.CreateTable(ctx, db, orders); err != nil {
		fmt.Printf("failed to create orders table: %s\n", err)
		return
	}

	// orders has no generated column accessors, so its columns are looked up by name.
	// That lookup validates them against the descriptor as the query is assembled.
	orderUserID, err := orders.Column("user_id")
	if err != nil {
		fmt.Printf("failed to find orders.user_id: %s\n", err)
		return
	}
	total, err := orders.Column("total")
	if err != nil {
		fmt.Printf("failed to find orders.total: %s\n", err)
		return
	}
	// Populate both tables through the typed rasql API.
	if _, err := rasql.Insert(ctx, db, users, UserRow{ID: 1, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}
	for _, order := range []orderRow{
		{ID: 1, UserID: 1, Total: 50},
		{ID: 2, UserID: 1, Total: 10},
	} {
		if _, err := rasql.Insert(ctx, db, orders, order); err != nil {
			fmt.Printf("failed to insert order: %s\n", err)
			return
		}
	}

	// DecodeFrom maps the selected names into orderSummary's exported fields.
	// SQL: SELECT users.id AS user_id, users.email FROM users INNER JOIN orders ON users.id = orders.user_id WHERE orders.total > ? ORDER BY orders.total DESC (argument: 20)
	rows, err := rasql.DecodeFrom[orderSummary](users).
		Join(rasql.InnerJoin(orders, query.Equal(users.ID(), orderUserID))).
		Project(query.Project(users.ID()).As("user_id"), query.Project(users.Email())).
		Where(query.GreaterThan(total, query.Bind(20))).
		Order(query.Desc(total)).
		Query(ctx, db)
	if err != nil {
		fmt.Printf("failed to build order totals query: %s\n", err)
		return
	}
	for summary, err := range rows {
		if err != nil {
			fmt.Printf("failed to query order totals: %s\n", err)
			return
		}
		fmt.Println(summary.UserID, summary.Email)
	}

	// Output:
	// 1 ada@example.com
}
```
source: [examples/rasql_dynamic_projection_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_dynamic_projection_example_test.go)
<!-- END INCLUDE -->

`Join` takes `rasql.InnerJoin` or `rasql.LeftJoin` with the join condition; the `query` versions take a `query.TableRef` for dynamic code. `Project` selects expressions rather than plain column names, and `As` renames one so it lines up with a field of the result type. Because the projection is explicit here, the result type only needs fields for the columns actually selected.

## See the SQL without a database

`rasql.New` accepts any `rasql.Handle`, not only `*sql.DB` and `*sql.Tx`. A few lines of debug implementation print statements instead of running them, which is useful for checking what a builder produces against each dialect.

<!-- INCLUDE(examples/rasql_debug_query_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
)

// statementPrinter is a debug-only rasql.Handle. It follows the same
// QueryContext contract as *sql.DB, but prints statements instead of running them.
type statementPrinter struct{}

func (statementPrinter) QueryContext(_ context.Context, query string, arguments ...any) (*sql.Rows, error) {
	fmt.Println(query)
	fmt.Printf("%v\n", arguments)
	return nil, nil
}

func (statementPrinter) ExecContext(_ context.Context, query string, arguments ...any) (sql.Result, error) {
	fmt.Println(query)
	fmt.Printf("%v\n", arguments)
	return nil, fmt.Errorf("statementPrinter does not execute statements")
}

func Example_rasql_debug_query() {
	// This example prints the SQL for a typed query without opening a database.
	// rasql.New accepts *sql.DB, *sql.Tx, or another rasql.Handle. This
	// debug Handle lets the example show the generated statement without a database.
	db, err := rasql.New(statementPrinter{}, dialect.PostgreSQL())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}

	// users is declared in query_example_tables_test.go with the shape rasqlgen
	// emits; an application would write store.Users() instead.
	count := 0
	rows, err := rasql.SelectFrom(users).WhereEqual(users.ID(), 42).Query(context.Background(), db)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	for _, err := range rows {
		if err != nil {
			fmt.Printf("failed to query users: %s\n", err)
			return
		}
		count++
	}

	fmt.Printf("%d result rows\n", count)

	// Output:
	// SELECT "users"."id", "users"."email" FROM "users" WHERE ("users"."id" = $1)
	// [42]
	// 0 result rows
}
```
source: [examples/rasql_debug_query_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_debug_query_example_test.go)
<!-- END INCLUDE -->

A debug `Handle` may return `nil` rows after logging; `dynamic.Scan` treats that as an empty result rather than an error. When only the SQL is wanted and no execution at all, `Build(d)` returns it from the dialect alone, with no `rasql.DB` needed.


## Next

[Writing rows](06-writing.md) covers inserts, updates, and deletes, and [Static templates](07-templates.md) covers fixed SQL text with named binds.
