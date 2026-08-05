# Querying

`rasql` reads rows through a fluent builder. Start from `rasql.SelectFrom` when the result has a table's row type, and from `rasql.DecodeFrom` when a join or projection produces a shape of its own.

Columns come from the generated table value, so `users.ID` is a `query.Column` already bound to the `users` table. A misspelled `users.Emial` is a compile error rather than a failed query, which [What the column fields catch](06-rasqlgen.md#what-the-column-fields-catch) demonstrates along with the cases that still fail at run time.

Every builder is immutable. Each call returns a new builder, so a partly built query can be shared or reused without one caller's `Limit` leaking into another's.

## Operation reference

The tables in this section enumerate every operation the public API offers. The sections after them show the common ones in use.

### Statements

| Operation | Entry point | Result |
| --- | --- | --- |
| `SELECT` decoded as a table's row type | `rasql.SelectFrom(client, table)` | `TypedSelectBuilder[T]` |
| `SELECT` decoded as a custom type | `rasql.DecodeFrom[R](client, table)` | `TypedSelectBuilder[R]` |
| `SELECT` decoded from a table with no row type | `rasql.DecodeQueryFrom[R](client, queryTable)` | `TypedSelectBuilder[R]` |
| `SELECT` without decoding | `client.SelectFrom(table.QueryTable())` | `SelectBuilder`, yielding `row.Row` |
| `INSERT` of one typed row | `rasql.Insert(ctx, client, table, value)` | `sql.Result` |
| `INSERT` with database defaults | `rasql.InsertWithOptions(ctx, client, table, value, rasql.DefaultColumns(...))` | `sql.Result` |
| `UPDATE` of one typed row by primary key | `rasql.Update(ctx, client, table, value)` | `sql.Result` |
| `DELETE` by predicate | `rasql.DeleteFrom(client, table)` | `DeleteBuilder` |
| `CREATE TABLE` plus its indexes | `rasql.Create(ctx, client, table)` | `error` |
| Upsert, `RETURNING`, partial update | `query.New…` then `client.Exec(ctx, statement)` | `sql.Result` |
| Compiled [static template](05-templates.md) | `client.ExecRendered(ctx, statement)` | `sql.Result` |

Writes are covered in [Writing rows](04-writing.md); the rest of this page covers reads.

### Select builder methods

`✓` marks the builders that carry the method. The typed builder comes from `SelectFrom`, `DecodeFrom`, and `DecodeQueryFrom`; the untyped one from `client.SelectFrom`.

The two builders differ in how they name a column. The typed builder takes a `query.Column`, usually a generated field such as `users.ID`, so a wrong name does not compile and a join can order by a column of any table in the statement. The untyped builder has exactly one table and no generated columns, so it keeps plain names.

| Method | Effect | Typed | Untyped |
| --- | --- | --- | --- |
| `Select(names…)` | Adds primary-table columns by name. | | ✓ |
| `Project(projections…)` | Adds projections built with `query.Project`. | ✓ | ✓ |
| `Join(joins…)` | Adds a join built with `rasql.InnerJoin` or `rasql.LeftJoin`. | ✓ | ✓ |
| `Where(expression)` | Sets the predicate from a `query` expression. | ✓ | ✓ |
| `WhereEqual(column, value)` | Sets `column = value` for a `query.Column`. | ✓ | |
| `WhereEqual(name, value)` | Sets `column = value` for a primary-table column. | | ✓ |
| `Order(orders…)` | Adds ordering built with `query.Asc` or `query.Desc`. | ✓ | ✓ |
| `OrderAsc(column)`, `OrderDesc(column)` | Adds ordering for a `query.Column`. | ✓ | |
| `OrderAsc(name)`, `OrderDesc(name)` | Adds ordering for a primary-table column. | | ✓ |
| `Limit(n)`, `Offset(n)` | Pages the result. | ✓ | ✓ |
| `Build()` | Renders `render.Statement` without executing. | ✓ | ✓ |
| `Query(ctx)` | Executes and returns a rangeable `iter.Seq2`; use it for a large result or an early stop. | ✓ | ✓ |
| `All(ctx)` | Executes and collects `[]T`; use it when the whole result fits in memory. | ✓ | |
| `One(ctx)` | Executes and returns one `T`; returns `rasql.ErrNoRows` for zero rows or `rasql.ErrMultipleRows` for more than one. | ✓ | |

`Where` and `WhereEqual` each replace the predicate set before them; combine conditions with `query.And` or `query.Or` rather than by calling them twice.

### Delete builder methods

| Method | Effect |
| --- | --- |
| `Where(expression)` | Sets the predicate from a `query` expression. |
| `WhereEqual(column, value)` | Sets `column = value` for a `query.Column` of the target table. |
| `Build()` | Renders `render.Statement` without executing. |
| `Exec(ctx)` | Executes and returns `sql.Result`. |

### Where conditions

Every constructor below takes and returns `query.Expression`, so conditions nest freely.

| Constructor | Renders |
| --- | --- |
| `query.Equal(left, right)` | `left = right` |
| `query.NotEqual(left, right)` | `left <> right` |
| `query.GreaterThan(left, right)` | `left > right` |
| `query.GreaterThanOrEqual(left, right)` | `left >= right` |
| `query.LessThan(left, right)` | `left < right` |
| `query.LessThanOrEqual(left, right)` | `left <= right` |
| `query.Like(left, right)` | `left LIKE right` |
| `query.Compare(left, operator, right)` | Any of the operators above, named by a `query.Operator…` constant. |
| `query.IsNull(expression)` | `expression IS NULL` |
| `query.IsNotNull(expression)` | `expression IS NOT NULL` |
| `query.And(expressions…)` | `(a AND b …)` |
| `query.Or(expressions…)` | `(a OR b …)` |
| `query.Negate(expression)` | `NOT (expression)` |

### Operands

| Constructor | Produces |
| --- | --- |
| `table.Field` | A generated column field, such as `users.ID`, checked by the compiler. |
| `table.Column(name)` | A column looked up by name and validated against the descriptor. |
| `query.Bind(value)` | A bound argument, rendered as the dialect's placeholder. |
| `query.Excluded(column)` | The proposed value of a column in an upsert. |

### Projections, joins, and ordering

| Constructor | Produces |
| --- | --- |
| `query.Project(expression)` | A projected expression. |
| `query.Project(expression).As(alias)` | The same projection under a result name. |
| `rasql.InnerJoin(table, on)` | An inner join on a typed table with its condition. |
| `rasql.LeftJoin(table, on)` | A left outer join on a typed table with its condition. |
| `query.InnerJoin(queryTable, on)`, `query.LeftJoin(queryTable, on)` | The same joins on a `query.Table`, for dynamic code. |
| `query.Asc(expression)`, `query.Desc(expression)` | Ordering for `Order`. |

### Statement constructors

The builders cover the common statements. These constructors build the same statements directly, and `client.Exec` runs any of them.

| Constructor | Statement |
| --- | --- |
| `query.NewSelect(from, projections…)` | `SELECT` |
| `query.NewInsert(into, columns, values)` | `INSERT` |
| `query.NewUpdate(table, assignments…)` | `UPDATE`, with `query.Set(column, expression)` per assignment. |
| `query.NewDelete(from)` | `DELETE` |
| `query.NewUpsert(insert, conflictColumns, assignments)` | Insert on conflict update. A non-empty `conflictColumns` requires `dialect.CapabilityConflictTarget`; MySQL lacks it and rejects the statement. |

Each statement is refined by `With…` methods: `WithJoin`, `WithWhere`, `WithOrder`, `WithLimit`, and `WithOffset` on `Select`, `WithWhere` on `Update` and `Delete`, and `WithReturning` on every write. Each returns a new validated statement rather than changing the one it was called on.

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
	defer database.Close()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A Client couples a database handle with the dialect used to render SQL.
	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}
	// Create the table described by the generated users descriptor.
	if err := rasql.Create(ctx, client, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	// Use rasql.Insert for each fixture row so setup follows the public API.
	for _, user := range []UserRow{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@example.com"},
	} {
		if _, err := rasql.Insert(ctx, client, users, user); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// SelectFrom knows the UsersRow result type from users. Query yields decoded
	// rows directly, so the loop does not need manual scanning or conversion.
	rows, err := rasql.SelectFrom(client, users).
		OrderAsc(users.Email).
		Offset(1).
		Limit(2).
		Query(ctx)
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

```go
user, err := rasql.SelectFrom(client, users).WhereEqual(users.ID, id).One(ctx)
if errors.Is(err, rasql.ErrNoRows) {
	// no such user
}
```

`Build()` skips execution and returns the rendered `render.Statement`, which carries the SQL text and its ordered arguments. It is the direct way to log or test a statement.

## Filter, order, and page

`WhereEqual`, `OrderAsc`, and `OrderDesc` take a `query.Column` and cover the common cases without importing the `query` package. Generated tables expose one field per column, so `users.ID` is the whole reference. `Limit` and `Offset` page the result. The untyped builder from `client.SelectFrom` also has `Select`, which narrows the projection to named columns.

For anything richer, `Where` and `Order` accept expressions from the `query` package:

```go
rows, err := rasql.SelectFrom(client, users).
	Where(query.And(
		query.GreaterThan(users.ID, query.Bind(10)),
		query.IsNotNull(users.ID),
	)).
	Order(query.Desc(users.ID)).
	Query(ctx)
```

A generated field cannot name a column the table does not have, because the field would not exist. A table built at run time has no such fields, so `table.Column(name)` looks the column up in the descriptor and fails when the table has no such column; a typo surfaces while the query is being assembled rather than as a database error later. `query.Bind` marks a value as an argument; the renderer turns it into the dialect's placeholder and appends it to the argument list. No public API puts a value into SQL text.

## Alias a table for a self-join

`As` returns the table under an alias with every column field rebound to it, so the alias qualifies the columns reached through the aliased value:

```go
// employees is a generated table with id and manager_id columns.
manager, err := employees.As("manager")
if err != nil {
	return err
}
rows, err := rasql.SelectFrom(client, employees).
	Join(rasql.InnerJoin(manager, query.Equal(employees.ManagerID, manager.ID))).
	Query(ctx)
```

`employees.ID` still renders as `"employees"."id"`, while `manager.ID` renders as `"manager"."id"`. `As` fails when the alias is not a valid identifier.

[Where conditions](#where-conditions) lists every comparison, logical connective, and null test the expression set offers.

## Decode a custom shape

A join or a narrowed projection does not return a table's row type. `DecodeFrom` names the result type instead, and maps each selected column onto its fields, matching a `rasql` tag if present and the snake-cased field name otherwise. A result type with a `DecodeRow` method maps itself instead, which [the two mapping methods](06-rasqlgen.md#the-two-mapping-methods) covers. Use `DecodeQueryFrom` when the primary table is a bare `query.Table` with no Go row type.

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
	defer database.Close()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A Client couples a database handle with the dialect used to render SQL.
	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
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
	orders := rasql.MustTable[orderRow](schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
			{Name: "total", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	// Create both descriptors before querying their joined rows.
	if err := rasql.Create(ctx, client, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if err := rasql.Create(ctx, client, orders); err != nil {
		fmt.Printf("failed to create orders table: %s\n", err)
		return
	}

	// orders has no generated column fields, so its columns are looked up by name.
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
	if _, err := rasql.Insert(ctx, client, users, UserRow{ID: 1, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}
	for _, order := range []orderRow{
		{ID: 1, UserID: 1, Total: 50},
		{ID: 2, UserID: 1, Total: 10},
	} {
		if _, err := rasql.Insert(ctx, client, orders, order); err != nil {
			fmt.Printf("failed to insert order: %s\n", err)
			return
		}
	}

	// DecodeFrom maps the selected names into orderSummary's exported fields.
	rows, err := rasql.DecodeFrom[orderSummary](client, users).
		Join(rasql.InnerJoin(orders, query.Equal(users.ID, orderUserID))).
		Project(query.Project(users.ID).As("user_id"), query.Project(users.Email)).
		Where(query.GreaterThan(total, query.Bind(20))).
		Order(query.Desc(total)).
		Query(ctx)
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

`Join` takes `rasql.InnerJoin` or `rasql.LeftJoin` with the join condition; the `query` versions take a `query.Table` for dynamic code. `Project` selects expressions rather than plain column names, and `As` renames one so it lines up with a field of the result type. Because the projection is explicit here, the result type only needs fields for the columns actually selected.

## See the SQL without a database

`rasql.New` accepts any `rasql.Queryer`, not only `*sql.DB` and `*sql.Tx`. A few lines of debug implementation print statements instead of running them, which is useful for checking what a builder produces against each dialect.

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

// statementPrinter is a debug-only rasql.Queryer. It follows the same
// QueryContext contract as *sql.DB, but prints statements instead of running them.
type statementPrinter struct{}

func (statementPrinter) QueryContext(_ context.Context, query string, arguments ...any) (*sql.Rows, error) {
	fmt.Println(query)
	fmt.Printf("%v\n", arguments)
	return nil, nil
}

func Example_rasql_debug_query() {
	// This example prints the SQL for a typed query without opening a database.
	// rasql.New accepts *sql.DB, *sql.Tx, or another rasql.Queryer. This
	// debug Queryer lets the example show the generated statement without a database.
	client, err := rasql.New(statementPrinter{}, dialect.PostgreSQL())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}

	// users is declared in query_example_tables_test.go with the shape rasqlgen
	// emits; an application would write store.Users() instead.
	count := 0
	rows, err := rasql.SelectFrom(client, users).WhereEqual(users.ID, 42).Query(context.Background())
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

A debug `Queryer` may return `nil` rows after logging; `Client` treats that as an empty result rather than an error. When only the SQL is wanted and no execution at all, `Build()` returns it without a client.

## Next

[Writing rows](04-writing.md) covers inserts, updates, and deletes, and [Static templates](05-templates.md) covers fixed SQL text with named binds.
