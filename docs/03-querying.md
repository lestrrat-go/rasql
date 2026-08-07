# Querying

`rasql` reads rows through a fluent builder. Start from `rasql.SelectFrom` when the result has a table's row type, and from `rasql.DecodeFrom` when a join or projection produces a shape of its own.

Columns come from the generated table value, so `users.ID` is a `query.Column` already bound to the `users` table. A misspelled `users.Emial` is a compile error rather than a failed query, which [What the column fields catch](06-rasqlgen.md#what-the-column-fields-catch) demonstrates along with the cases that still fail at run time.

Every builder is immutable. Each call returns a new builder, so a partly built query can be shared or reused without one caller's `Limit` leaking into another's.

## Operation reference

The tables in this section enumerate every operation the public API offers. The sections after them show the common ones in use.

### Statements

| Operation | Entry point | Result |
| --- | --- | --- |
| `SELECT` decoded as a table's row type | `rasql.SelectFrom(table)` | `TypedSelectBuilder[T]` |
| `SELECT` decoded as a custom type | `rasql.DecodeFrom[R](table)` | `TypedSelectBuilder[R]` |
| `SELECT` decoded from a table with no row type | `rasql.DecodeQueryFrom[R](queryTable)` | `TypedSelectBuilder[R]` |
| `SELECT` without decoding | `rasql.SelectQueryFrom(table.QueryTable())` | `SelectBuilder`, yielding `row.Row` |
| `INSERT` of one typed row | `rasql.Insert(ctx, client, table, value)` | `sql.Result` |
| `INSERT` with database defaults | `rasql.InsertWithOptions(ctx, client, table, value, rasql.DefaultColumns(...))` | `sql.Result` |
| `INSERT` of several rows | `query.NewInsertRows(table.QueryTable(), columns, rows)` then `rasql.Exec(ctx, client, statement)` | `sql.Result` |
| `UPDATE` of one typed row by primary key | `rasql.Update(ctx, client, table, value)` | `sql.Result` |
| `DELETE` by predicate | `rasql.DeleteFrom(table)` | `DeleteBuilder` |
| `CREATE TABLE` plus its indexes | `rasql.Create(ctx, client, table)` | `error` |
| Upsert, partial update | `query.New…` then `rasql.Exec(ctx, client, statement)` | `sql.Result` |
| Write with `RETURNING` | `query.New….WithReturning(...)` then `rasql.QueryWrite(ctx, client, statement)` / `rasql.QueryWriteAll[T]` / `rasql.QueryWriteOne[T]` | `row.Row` or `[]T` / `T` |
| Compiled [static template](05-templates.md) | `client.ExecRendered(ctx, statement)` | `sql.Result` |

Writes are covered in [Writing rows](04-writing.md); the rest of this page covers reads.

### Select builder methods

`✓` marks the builders that carry the method. The typed builder comes from `SelectFrom`, `DecodeFrom`, and `DecodeQueryFrom`; the untyped one from `rasql.SelectQueryFrom`.

The two builders differ in how they name a column. The typed builder takes a `query.Column`, usually a generated field such as `users.ID`, so a wrong name does not compile and a join can order by a column of any table in the statement. The untyped builder has exactly one table and no generated columns, so it keeps plain names.

| Method | Effect | Typed | Untyped |
| --- | --- | --- | --- |
| `Select(names…)` | Adds primary-table columns by name. | | ✓ |
| `Project(projections…)` | Adds projections built with `query.Project`. | ✓ | ✓ |
| `Distinct()` | De-duplicates result rows. | ✓ | ✓ |
| `Join(joins…)` | Adds a join built with `rasql.InnerJoin` or `rasql.LeftJoin`. | ✓ | ✓ |
| `Where(expression)` | Adds a predicate from a `query` expression. | ✓ | ✓ |
| `WhereEqual(column, value)` | Adds `column = value` for a `query.Column`. | ✓ | |
| `WhereEqual(name, value)` | Adds `column = value` for a primary-table column. | | ✓ |
| `WhereIn(column, values…)` | Adds `column IN (values…)` for a `query.Column`, one placeholder per value. | ✓ | |
| `WhereIn(name, values…)` | Adds `column IN (values…)` for a primary-table column, one placeholder per value. | | ✓ |
| `GroupBy(expressions…)` | Adds grouping built with the basic query API. | ✓ | ✓ |
| `GroupByColumns(names…)` | Adds primary-table columns to the grouping by name. | | ✓ |
| `Having(expression)` | Adds a grouped predicate from a `query` expression; combines with `AND` like `Where`. | ✓ | ✓ |
| `Order(orders…)` | Adds ordering built with `query.Asc` or `query.Desc`. | ✓ | ✓ |
| `OrderAsc(column)`, `OrderDesc(column)` | Adds ordering for a `query.Column`. | ✓ | |
| `OrderAsc(name)`, `OrderDesc(name)` | Adds ordering for a primary-table column. | | ✓ |
| `Limit(n)`, `Offset(n)` | Pages the result. | ✓ | ✓ |
| `Build(d)` | Renders `render.Statement` for a `dialect.Dialect` without executing. | ✓ | ✓ |
| `Query(ctx, executor)` | Executes and returns a rangeable `iter.Seq2`; use it for a large result or an early stop. | ✓ | ✓ |
| `All(ctx, executor)` | Executes and collects `[]T`; use it when the whole result fits in memory. | ✓ | |
| `One(ctx, executor)` | Executes and returns one `T`; returns `rasql.ErrNoRows` for zero rows or `rasql.ErrMultipleRows` for more than one. | ✓ | |
| `Count(ctx, executor)` | Executes `COUNT(*)` over the matched rows in place of the builder's projections; rejects a builder with `Limit`, `Offset`, or `Distinct` set. | ✓ | ✓ |

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
| `WhereEqual(column, value)` | Adds `column = value` for a `query.Column` of the target table. |
| `WhereIn(column, values…)` | Adds `column IN (values…)` for a `query.Column` of the target table, one placeholder per value. |
| `Build(d)` | Renders `render.Statement` for a `dialect.Dialect` without executing. |
| `Exec(ctx, executor)` | Executes and returns `sql.Result`. |

`Where`, `WhereEqual`, and `WhereIn` accumulate on the delete builder the same way: repeated calls combine with `AND` in the order they were made. Each of them supplies the predicate that `Build` and `Exec` require, so a delete still needs one of them or an explicit `AllowAll`. `WhereIn` needs at least one value: an empty list makes `Build` and `Exec` return an error rather than render `IN ()`, which is not valid SQL in any supported dialect.

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
| `query.In(expression, values…)` | `expression IN (values…)` |
| `query.NotIn(expression, values…)` | `expression NOT IN (values…)` |
| `query.InSelect(expression, statement)` | `expression IN (SELECT …)` |
| `query.NotInSelect(expression, statement)` | `expression NOT IN (SELECT …)` |
| `query.Scalar(statement)` | `(SELECT …)`, used as a single value |
| `query.And(expressions…)` | `(a AND b …)` |
| `query.Or(expressions…)` | `(a OR b …)` |
| `query.Negate(expression)` | `NOT (expression)` |

The value list of `query.In` and `query.NotIn` takes expressions, the same freedom the comparison constructors give both of their operands. Each `query.Bind` value becomes its own placeholder, so a list of `N` bound values costs `N` arguments against the dialect's parameter limit. A non-value operand is accepted deliberately and costs no argument: a column such as `orders.UserID` renders as a quoted identifier, which is how a column-to-column test like `query.In(users.ID, orders.UserID)` is written. `query.InSelect` and `query.NotInSelect` take a `SELECT` statement in place of a value list, and [Subqueries](#subqueries) covers the placement rules and the one MySQL restriction that governs them. An empty value list is a validation error rather than `IN ()`, which is not valid SQL in any supported dialect. A membership test is an ordinary predicate, so the placement rules in [Aggregates](#aggregates) reach its operands as they reach the operands of a comparison.

### Operands

| Constructor | Produces |
| --- | --- |
| `table.Field` | A generated column field, such as `users.ID`, checked by the compiler. |
| `table.Column(name)` | A column looked up by name and validated against the descriptor. |
| `query.Bind(value)` | A bound argument, rendered as the dialect's placeholder. |
| `query.Excluded(column)` | The proposed value of a column in an upsert. |

### Aggregates

`Function` calls a SQL function on its arguments. `COUNT`, `SUM`, `MIN`, `MAX`, `AVG`, `COALESCE`, `LOWER`, `UPPER`, and `ABS` are the curated set of names `Call` accepts; any other name fails validation before it reaches SQL. `Call(FunctionName("LENGTH"), …)` fails for that reason: `LENGTH` is deliberately excluded, because PostgreSQL, MySQL, and SQLite disagree on whether it counts characters or bytes, and this package will not hide that difference behind one name. [Scalar functions](#scalar-functions) covers `COALESCE`, `LOWER`, `UPPER`, and `ABS`, plus `Func`, the escape hatch for a function this package does not curate. This section covers `COUNT`, `SUM`, `MIN`, `MAX`, and `AVG`, which aggregate.

| Constructor | Renders |
| --- | --- |
| `query.CountAll()` | `COUNT(*)` |
| `query.Count(expression)` | `COUNT(expression)` |
| `query.Sum(expression)` | `SUM(expression)` |
| `query.Min(expression)` | `MIN(expression)` |
| `query.Max(expression)` | `MAX(expression)` |
| `query.Avg(expression)` | `AVG(expression)` |
| `query.Call(name, arguments…)` | Any of the functions above, named by a `query.Function…` constant. |

Every function above aggregates, so validation accepts one only where SQL does, and reports a `query.ValidationError` everywhere else:

- A `SELECT` projection and a `HAVING` clause may call an aggregate. A `WHERE` clause, a `JOIN ON` condition, a `GROUP BY` clause, and every clause of an `INSERT`, `UPDATE`, `DELETE`, or upsert — including `RETURNING` — reject one.
- An aggregate must not contain another, at any depth: `query.Sum(query.Sum(column))` is refused, since no supported dialect runs it.
- An ungrouped projection set that calls an aggregate must not also read a column outside one, because reconciling the two needs `GROUP BY`. Project `query.CountAll()` on its own, or beside another aggregate, rather than beside `users.ID` — or build the statement with `query.NewGroupedSelect` and group by `users.ID`, which [Group rows](#group-rows) covers.
- An `ORDER BY` clause follows the projections. A statement that groups explicitly may read its grouping keys freely and may call an aggregate. Otherwise: when no projection aggregates, ordering reads columns freely and may not call an aggregate; when the projection set aggregates, the statement is one implicit group, so ordering may call an aggregate — `query.Asc(query.CountAll())` — and every column it reads has to sit inside one; `query.Asc(users.ID)` beside an aggregate projection is refused for the same `GROUP BY` reason as the projection rule above, unless the statement groups.
- A `HAVING` clause needs a statement that groups, which means one of two things: an explicit `GROUP BY`, or a projection set that aggregates and reads no column outside an aggregate, which is one group. Not every projection in that second set has to aggregate — a projection that reads no column, such as `query.Bind(7)`, may sit beside `query.CountAll()` and the set still counts as one group. A `HAVING` on a statement with neither is refused, so `Having` over a projection set that reads plain columns needs a `GroupBy` beside it. Without a `GROUP BY` the clause follows the same rule as `ORDER BY` over that one implicit group, so a column it reads has to sit inside an aggregate: `query.GreaterThan(query.CountAll(), query.Bind(1))` is accepted and `query.GreaterThan(users.ID, query.Bind(1))` is refused. A statement that groups explicitly drops that restriction and may read its grouping keys freely.
- Every rule above applies to the operands of a membership test, because `query.In` and `query.NotIn` are ordinary predicates rather than aggregates. `query.In(users.ID, query.Count(users.ID))` in a `WHERE` clause is refused exactly as `query.Equal(users.ID, query.Count(users.ID))` is, and a column read from an `IN` list counts as a column read outside an aggregate wherever a bare column would.

An aggregate has no result name of its own — PostgreSQL, MySQL, and SQLite each report a different one for an unaliased call — so a projection that will be decoded needs `.As(alias)` from [Projections, joins, and ordering](#projections-joins-and-ordering). `rasql.DecodeFrom[R]` maps an aliased aggregate onto a field of `R` the same way it maps any other projected column.

`query.Function.WithDistinct()` returns a copy of a call that evaluates its argument only once per distinct value, rendering `query.Count(users.ID).WithDistinct()` as `COUNT(DISTINCT users.id)`. It is a modifier on the argument, not a separate function name, so it applies to any of the aggregate constructors above; validation refuses it combined with `query.CountAll()`'s `*`, since `COUNT(DISTINCT *)` is not legal SQL. `DISTINCT` inside a call asks the function to combine one row per distinct argument value, which only an aggregate does, so validation refuses it on a curated scalar call — [Scalar functions](#scalar-functions) states that rule and what `query.Func` does with the modifier instead. `query.Count(column).WithDistinct()` counts the distinct non-NULL values of that one expression, which is not a count of the rows a `SELECT DISTINCT` returns: `COUNT` ignores NULL where `SELECT DISTINCT` keeps it as a value, and the call takes exactly one argument, so a distinct count over several projected columns has no form here. The derived table or CTE that would express one portably is unsupported. The builder's own `Count` in [Count rows](#count-rows) rejects a distinct builder for a reason of its own, because it would render `SELECT DISTINCT COUNT(*)`, which is always one row and never the number of distinct rows.

### Scalar functions

`COALESCE`, `LOWER`, `UPPER`, and `ABS` are scalar rather than aggregate: a call to one of them is legal wherever any expression is, with no placement rule of its own.

| Constructor | Renders |
| --- | --- |
| `query.Coalesce(expressions…)` | `COALESCE(expressions…)`, at least two expressions |
| `query.Lower(expression)` | `LOWER(expression)` |
| `query.Upper(expression)` | `UPPER(expression)` |
| `query.Abs(expression)` | `ABS(expression)` |
| `query.Func(name, arguments…)` | `name(arguments…)`, the escape hatch below |
| `query.Call(name, arguments…)` | Any of `Coalesce`, `Lower`, `Upper`, or `Abs` above, named by a `query.Function…` constant. |
| `function.Aggregates()` | Reports whether a built `query.Function` aggregates, so a caller can tell the two classes apart without repeating the constant list. A `query.Func` call reports `false` whatever its name. |

A scalar call carries the placement context of wherever it sits unchanged into its arguments, so every rule in [Aggregates](#aggregates) falls out for free rather than needing its own version:

- `query.Coalesce(query.Sum(x), query.Bind(0))` is accepted in a `SELECT` projection or a `HAVING` clause, the same places a bare `query.Sum(x)` is, and refused in a `WHERE` clause or a `JOIN ON` condition for the same reason a bare `query.Sum(x)` is.
- `query.Sum(query.Coalesce(x, query.Bind(0)))` is accepted: the scalar call inside the aggregate's argument is not itself an aggregate, so it does not trip the "aggregate inside another aggregate" rule. `query.Sum(query.Coalesce(query.Sum(x), query.Bind(0)))` is still refused, because the inner `Sum` is nested two aggregates deep.
- `query.Lower(users.Email)` counts as reading a bare column, exactly as `users.Email` on its own would: it is refused beside `query.CountAll()` in an ungrouped projection set and accepted once the statement groups, the same rule [Aggregates](#aggregates) states for a plain column.
- A scalar call reaches `INSERT` values, `SET` assignments, and `RETURNING`, which an aggregate cannot: `SET email = LOWER(email)` and `INSERT … VALUES (COALESCE(?, ?), …)`, from `query.Coalesce(query.Bind("ada@example.com"), query.Bind(""))`, both validate and render. The `SET` half reads the target table's own `email` column, which an `UPDATE` may do because it has a row in hand. An `INSERT` `VALUES` row has no such row, so in SQL it cannot read the target table's columns at all; give a call there bound values or other expressions that read no column of the table being written.
- `query.Function.WithDistinct()` is the one modifier from [Aggregates](#aggregates) that does not carry over: validation refuses `query.Lower(x).WithDistinct()`, because `LOWER(DISTINCT x)` is a syntax error on all three dialects. `DISTINCT` inside a call asks the function to combine one row per distinct argument value, and only an aggregate combines rows at all.

SQLite's `LOWER`/`UPPER` fold ASCII letters only, while PostgreSQL and MySQL fold according to the server's collation, so a case-insensitive match on non-ASCII text is not portable across the three dialects. MySQL's `LOWER`/`UPPER` leave a binary-typed argument unchanged. A projected scalar call has no portable result name any more than an aggregate does, so a projection that will be decoded needs `.As(alias)` the same way [Aggregates](#aggregates) states.

MySQL also changes the scale a coalesced decimal decodes at. `query.Coalesce(amount, query.Bind("0.0000"))` over a `DECIMAL(19,4)` column decodes with 30 digits right of the decimal point rather than 4, because MySQL fixes the result type while it prepares the statement and a placeholder carries no scale of its own at that point, so the call becomes the widest decimal the server has, `DECIMAL(65,30)`. The number is unchanged and only its trailing zeroes differ, but a caller comparing the decoded string against a literal has to expect the widened form — see [Logical column types](02-schema.md#logical-column-types), which states the narrower rule a plain decimal column follows. Coalescing against another decimal expression of the same scale, such as a second column, keeps that scale, and so does a driver that interpolates its arguments into the SQL text client-side rather than sending them as placeholders. PostgreSQL and SQLite return the value at its own scale in every case. The same widening reaches a `query.Func` call that mixes a decimal column with a bound value only when the named function's own MySQL type rules resolve a common decimal result type across all of its arguments, as `IFNULL`, `GREATEST`, and `LEAST` do. A function that types its result from a single argument keeps that argument's scale instead: MySQL types `NULLIF` from its first argument, so `query.Func("NULLIF", amount, query.Bind("0.0000"))` over a `DECIMAL(19,4)` column still decodes at scale 4, while the same call with the placeholder first widens. The rule is about which argument the result takes its type from, not about the function name. A function that returns a string or an integer, such as `CONCAT`, has no decimal result scale to widen and is unaffected.

<!-- INCLUDE(examples/rasql_scalar_function_example_test.go) -->
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

func Example_rasql_scalar_function() {
	// This example looks a member up by email regardless of case with LOWER,
	// then reads every member's display name, falling back to their email
	// with COALESCE when no nickname is set.
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
	// A typed descriptor makes members usable with rasql.Insert. Nickname is
	// nullable, so COALESCE below has a real NULL to fall back from.
	type memberRow struct {
		ID       int     `rasql:"id"`
		Email    string  `rasql:"email"`
		Nickname *string `rasql:"nickname"`
	}
	// A local result type holds the decoded id and display name.
	type memberName struct {
		ID   int64
		Name string
	}
	members := rasql.MustTable[memberRow](schema.Table{
		Name: "members",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
			{Name: "nickname", Type: schema.TypeText, Nullable: true},
		},
		PrimaryKey: []string{"id"},
	})
	if err := rasql.Create(ctx, client, members); err != nil {
		fmt.Printf("failed to create members table: %s\n", err)
		return
	}

	// members has no generated column fields, so its columns are looked up by
	// name. That lookup validates them against the descriptor as the query is
	// assembled.
	id, err := members.Column("id")
	if err != nil {
		fmt.Printf("failed to find members.id: %s\n", err)
		return
	}
	email, err := members.Column("email")
	if err != nil {
		fmt.Printf("failed to find members.email: %s\n", err)
		return
	}
	nickname, err := members.Column("nickname")
	if err != nil {
		fmt.Printf("failed to find members.nickname: %s\n", err)
		return
	}

	nick := "Ada"
	for _, member := range []memberRow{
		{ID: 1, Email: "Ada@Example.com", Nickname: &nick},
		{ID: 2, Email: "bob@example.com", Nickname: nil},
	} {
		if _, err := rasql.Insert(ctx, client, members, member); err != nil {
			fmt.Printf("failed to insert member: %s\n", err)
			return
		}
	}

	// LOWER(email) matches "Ada@Example.com" against the lower-case literal a
	// caller would type, regardless of how the stored value was cased.
	byEmail, err := rasql.DecodeFrom[memberName](members).
		Project(query.Project(id), query.Project(query.Coalesce(nickname, email)).As("name")).
		Where(query.Equal(query.Lower(email), query.Bind("ada@example.com"))).
		Query(ctx, client)
	if err != nil {
		fmt.Printf("failed to query member by email: %s\n", err)
		return
	}
	for member, err := range byEmail {
		if err != nil {
			fmt.Printf("failed to query member by email: %s\n", err)
			return
		}
		fmt.Println(member.Name)
	}

	// COALESCE(nickname, email) reads every member's display name, falling
	// back to the email once nickname is NULL.
	names, err := rasql.DecodeFrom[memberName](members).
		Project(query.Project(id), query.Project(query.Coalesce(nickname, email)).As("name")).
		OrderAsc(id).
		Query(ctx, client)
	if err != nil {
		fmt.Printf("failed to query member names: %s\n", err)
		return
	}
	for member, err := range names {
		if err != nil {
			fmt.Printf("failed to query member names: %s\n", err)
			return
		}
		fmt.Println(member.ID, member.Name)
	}

	// Output:
	// Ada
	// 1 Ada
	// 2 bob@example.com
}
```
source: [examples/rasql_scalar_function_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_scalar_function_example_test.go)
<!-- END INCLUDE -->

#### Escape hatch for an uncurated function

`query.Func(name, arguments…)` calls the SQL function named `name` on `arguments`, and `name` is any string rather than one of the `query.Function…` constants — this is what reaches a function this package does not curate without waiting for a change to rasql, such as `query.Func("jsonb_path_query", doc, query.Bind("$.a"))`.

`Func` guarantees only that `name` is a legal SQL identifier before it reaches SQL text, reusing the same identifier rule `schema.ValidateIdentifier` applies to a column or table name; validation reports a `query.ValidationError` naming the offending input when it is not. `Func` guarantees nothing about `name` beyond that: whether the named function exists on the target database, what arguments it takes, what it does, and whether it renders identically across PostgreSQL, MySQL, and SQLite are entirely the caller's responsibility. Every argument still goes through the ordinary expression nodes, so a value passed with `query.Bind` still travels as a placeholder rather than as SQL text — only the function's own name reaches SQL unescaped, and only once validation has confirmed it is a legal identifier. A call built with `Func` is always treated as scalar and reaches every clause a curated scalar call does, with no arity check and no aggregate placement rule, even when `name` happens to match a curated aggregate such as `"SUM"`; `function.Aggregates()` reports `false` for it for the same reason. Prefer `Call` with a `query.Function…` constant, or `Coalesce`, `Lower`, `Upper`, or `Abs`, whenever the function is one of them.

`query.Function.WithDistinct()` is the one rule a `Func` call does not inherit from a curated scalar call: it is carried through to the rendered SQL rather than refused, so `query.Func("group_concat", tag).WithDistinct()` renders as `group_concat(DISTINCT tag)`. rasql does not know whether the named function aggregates, and `DISTINCT` is the only way to reach an aggregate it does not curate; whether the target database accepts the call is the caller's responsibility, exactly as everything else about a `Func` name is.

### Subqueries

`query.Subquery` is a `SELECT` statement used as an expression: `query.Scalar(statement)` uses it as a single value, and `query.InSelect`/`query.NotInSelect` use it as the right-hand side of a membership test. In every form, `statement` must project exactly one expression — validation reports the count when it does not — because `x > (SELECT a, b …)` is as invalid as `x IN (SELECT a, b …)`.

A subquery is legal in the projections, `JOIN ON` conditions, `WHERE` clause, `GROUP BY` clause, `HAVING` clause, and `ORDER BY` clause of a `SELECT` statement, and nowhere else: every clause of an `INSERT`, `UPDATE`, `DELETE`, or upsert — including `RETURNING` — refuses one, the same way those clauses refuse an aggregate. A subquery reads no table of the statement that encloses it: every column it names must belong to its own `FROM` or joins, so a subquery that reads an enclosing table is refused rather than treated as a correlation. A subquery may nest inside another subquery to any depth.

```go
owned, err := query.NewSelect(projects.QueryTable(), query.Project(projects.ID))
owned, err = owned.WithWhere(query.Equal(projects.OwnerID, query.Bind(7)))

allTasks, err := tasks.As("all_tasks")
average, err := query.NewSelect(allTasks.QueryTable(), query.Project(query.Avg(allTasks.Priority)))

rows, err := rasql.DecodeFrom[taskSummary](tasks).
	Project(query.Project(tasks.ID), query.Project(tasks.Title)).
	Where(query.InSelect(tasks.ProjectID, owned)).
	Where(query.GreaterThanOrEqual(tasks.Priority, query.Scalar(average))).
	OrderAsc(tasks.ID).
	All(ctx, client)
```

`query.InSelect` costs no argument per candidate, unlike `query.In`, so a set of any size fits within the dialect's parameter limit; the arguments a subquery binds join the enclosing statement's argument list at the position the subquery occupies, so placeholder numbering stays correct in every dialect. MySQL refuses a `LIMIT` or an `OFFSET` on the statement given to `InSelect` or `NotInSelect` — error 1235 — so rendering for MySQL reports an error instead of sending SQL the server would reject; PostgreSQL and SQLite accept it. That restriction does not apply to `Scalar`, which MySQL accepts with a `LIMIT`.

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

The builders cover the common statements. These constructors build the same statements directly. `rasql.Query(ctx, executor, statement)` runs a `Select`, and `rasql.Exec(ctx, executor, statement)` runs a write that carries no `RETURNING` clause. Both are free functions over an `Executor`, so the same statement runs against a `Client` or a `Tx`. A write refined with `WithReturning` reads its rows back through `rasql.QueryWrite`, `rasql.QueryWriteAll[T]`, or `rasql.QueryWriteOne[T]`, because `rasql.Exec` rejects it.

| Constructor | Statement |
| --- | --- |
| `query.NewSelect(from, projections…)` | `SELECT` |
| `query.NewGroupedSelect(from, groupBy, projections…)` | `SELECT` that groups; needed when the projections mix an aggregate with a bare column, which `NewSelect` refuses. |
| `query.NewJoinedSelect(from, joins, groupBy, projections…)` | `SELECT` that carries its joins from the start; needed when a projection or a grouping expression reads a joined table, which the other two refuse because they validate before `WithJoin` can run. Pass a nil `groupBy` when the statement does not group. |
| `query.NewInsert(into, columns, values)` | `INSERT` |
| `query.NewUpdate(table, assignments…)` | `UPDATE`, with `query.Set(column, expression)` per assignment. |
| `query.NewDelete(from)` | `DELETE` |
| `query.NewUpsert(insert, conflictColumns, assignments)` | Insert on conflict update. A non-empty `conflictColumns` requires `dialect.CapabilityConflictTarget`; MySQL lacks it and rejects the statement. |

Each statement is refined by `With…` methods: `WithJoin`, `WithWhere`, `WithGroupBy`, `WithHaving`, `WithOrder`, `WithLimit`, `WithOffset`, and `WithDistinct` on `Select`, `WithWhere` on `Update` and `Delete`, and `WithReturning` on every write, which [Reading a `RETURNING` clause](04-writing.md#reading-a-returning-clause) covers. Each returns a new validated statement rather than changing the one it was called on.

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
	rows, err := rasql.SelectFrom(users).
		OrderAsc(users.Email).
		Offset(1).
		Limit(2).
		Query(ctx, client)
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
user, err := rasql.SelectFrom(users).WhereEqual(users.ID, id).One(ctx, client)
if errors.Is(err, rasql.ErrNoRows) {
	// no such user
}
```

`Build(d)` skips execution and returns the rendered `render.Statement`, which carries the SQL text and its ordered arguments. It takes a `dialect.Dialect` rather than an `Executor`, because rendering needs the dialect and nothing else. It is the direct way to log or test a statement.

## Filter, order, and page

`WhereEqual`, `OrderAsc`, and `OrderDesc` take a `query.Column` and cover the common cases without importing the `query` package. Generated tables expose one field per column, so `users.ID` is the whole reference. `Limit` and `Offset` page the result. The untyped builder from `rasql.SelectQueryFrom` also has `Select`, which narrows the projection to named columns.

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

	// WhereIn binds one placeholder per value and skips the users whose id is
	// not in the list. The list must hold at least one value: an empty one makes
	// Query return an error instead of rendering IN (), which is not valid SQL.
	rows, err := rasql.SelectFrom(users).
		WhereIn(users.ID, 1, 3).
		OrderAsc(users.ID).
		Query(ctx, client)
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

```go
rows, err := rasql.SelectFrom(users).
	Where(query.And(
		query.GreaterThan(users.ID, query.Bind(10)),
		query.IsNotNull(users.ID),
	)).
	Order(query.Desc(users.ID)).
	Query(ctx, client)
```

A generated field cannot name a column the table does not have, because the field would not exist. A table built at run time has no such fields, so `table.Column(name)` looks the column up in the descriptor and fails when the table has no such column; a typo surfaces while the query is being assembled rather than as a database error later. `query.Bind` marks a value as an argument; the renderer turns it into the dialect's placeholder and appends it to the argument list. No public API puts a value into SQL text.

## Filter with a subquery

`query.InSelect` and `query.Scalar` take a `SELECT` statement as the right-hand side of a predicate, in place of a value list or a bound value. Each subquery is validated and rendered as its own statement; [Subqueries](#subqueries) covers the placement rules, including MySQL's restriction on `LIMIT` inside an `InSelect` statement.

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
		Amount int `rasql:"amount"`
	}
	// A local result type projects only orders columns, so no join is needed:
	// both subqueries below run as their own SELECT, never as part of this one.
	type orderSummary struct {
		UserID int64
		Amount int64
	}
	orders := rasql.MustTable[orderRow](schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
			{Name: "amount", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	// Create both descriptors before querying orders against the users subquery.
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
		if _, err := rasql.Insert(ctx, client, users, user); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}
	for _, order := range []orderRow{
		{ID: 1, UserID: 1, Amount: 80},
		{ID: 2, UserID: 2, Amount: 20},
		{ID: 3, UserID: 3, Amount: 100},
	} {
		if _, err := rasql.Insert(ctx, client, orders, order); err != nil {
			fmt.Printf("failed to insert order: %s\n", err)
			return
		}
	}

	// domainUsers selects the id of every user whose email ends in the chosen
	// domain. It reads no table of the enclosing statement, so it validates and
	// renders as its own SELECT.
	domainUsers, err := query.NewSelect(users.QueryTable(), query.Project(users.ID))
	if err != nil {
		fmt.Printf("failed to build domain-users subquery: %s\n", err)
		return
	}
	domainUsers, err = domainUsers.WithWhere(query.Like(users.Email, query.Bind("%@example.com")))
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
	average, err := query.NewSelect(allOrders.QueryTable(), query.Project(query.Avg(allOrdersAmount)))
	if err != nil {
		fmt.Printf("failed to build average subquery: %s\n", err)
		return
	}

	// InSelect keeps orders placed by a domain user without costing one
	// argument per candidate id, and Scalar compares amount against the
	// average of every order.
	rows, err := rasql.DecodeFrom[orderSummary](orders).
		Project(query.Project(orderUserID).As("user_id"), query.Project(amount)).
		Where(query.InSelect(orderUserID, domainUsers)).
		Where(query.GreaterThanOrEqual(amount, query.Scalar(average))).
		Order(query.Asc(amount)).
		Query(ctx, client)
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

	// Count runs COUNT(*) over the builder's WHERE and joins, without decoding
	// any row into a UserRow. It rejects a builder with Limit or Offset set,
	// since a count of a paged statement is not the count the caller asked for.
	total, err := rasql.SelectFrom(users).Count(ctx, client)
	if err != nil {
		fmt.Printf("failed to count users: %s\n", err)
		return
	}
	fmt.Println("total:", total)

	filtered, err := rasql.SelectFrom(users).WhereEqual(users.ID, 2).Count(ctx, client)
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

`Count` rejects a builder that sets `Limit` or `Offset`, because a count of a paged statement is not the count the caller built the statement to ask for; count an unpaged builder, then page a copy of it for the rows. `SUM` and `AVG` have no equivalent helper, because their result types are not portable across dialects — project them with `query.Sum` or `query.Avg` and decode through `rasql.DecodeFrom[R]` instead, as [Aggregates](#aggregates) covers.

## Group rows

`GroupBy` adds a `GROUP BY` clause, which is what lets a projection set mix a bare column with an aggregate: [Aggregates](#aggregates) refuses that combination without one. `Having` adds a `HAVING` clause, filtering groups after aggregation the way `Where` filters rows before it; repeated calls combine with `AND` in the order they were made, exactly as `Where` does. `Having` needs groups to filter, so it requires either a `GroupBy` or a projection set that aggregates and reads no column outside an aggregate, a set in which a projection reading no column — a bound value, say — may sit beside the aggregate. [Aggregates](#aggregates) states what each of those two cases allows the clause to read.

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
	defer database.Close()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A Client couples a database handle with the dialect used to render SQL.
	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
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
	tasks := rasql.MustTable[taskRow](schema.Table{
		Name: "tasks",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "status", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	if err := rasql.Create(ctx, client, tasks); err != nil {
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
		if _, err := rasql.Insert(ctx, client, tasks, task); err != nil {
			fmt.Printf("failed to insert task: %s\n", err)
			return
		}
	}

	// tasks has no generated column field for status, so it is looked up by
	// name. That lookup validates it against the descriptor as the query is
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
	rows, err := rasql.DecodeFrom[statusCount](tasks).
		Project(query.Project(status), query.Project(query.CountAll()).As("total")).
		GroupBy(status).
		Having(query.GreaterThan(query.CountAll(), query.Bind(1))).
		Order(query.Asc(status)).
		Query(ctx, client)
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

The untyped builder groups by a primary-table column name with `GroupByColumns`, the counterpart to `Select`'s `names…` form, without importing the `query` package.

## Select distinct rows

`Distinct()` adds `DISTINCT` right after `SELECT`, so the statement returns one row per distinct combination of its projected values. It is meaningful mainly beside a narrowed projection: `rasql.SelectFrom[T]` already selects every column of the table, including its primary key, which makes every row unique before `DISTINCT` runs. Use `rasql.DecodeFrom[R]` with `Project`, or the untyped builder's `Select` with specific column names, to narrow the projection first.

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
	defer database.Close()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A Client couples a database handle with the dialect used to render SQL.
	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
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
	orders := rasql.MustTable[orderRow](schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	if err := rasql.Create(ctx, client, orders); err != nil {
		fmt.Printf("failed to create orders table: %s\n", err)
		return
	}
	for _, order := range []orderRow{
		{ID: 1, UserID: 1},
		{ID: 2, UserID: 2},
		{ID: 3, UserID: 1},
	} {
		if _, err := rasql.Insert(ctx, client, orders, order); err != nil {
			fmt.Printf("failed to insert order: %s\n", err)
			return
		}
	}

	// orders has no generated column field for user_id, so it is looked up by
	// name. That lookup validates it against the descriptor as the query is
	// assembled.
	orderUserID, err := orders.Column("user_id")
	if err != nil {
		fmt.Printf("failed to find orders.user_id: %s\n", err)
		return
	}

	// Distinct is meaningful here because Project narrows the result to
	// user_id alone; SelectFrom would already select the orders primary key,
	// which makes every row unique before DISTINCT runs.
	rows, err := rasql.DecodeFrom[orderingUser](orders).
		Project(query.Project(orderUserID).As("user_id")).
		Distinct().
		Order(query.Asc(orderUserID)).
		Query(ctx, client)
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

`Count` rejects a distinct builder, because it replaces the projections with `COUNT(*)`: `SELECT DISTINCT COUNT(*)` is always exactly one row, never the number of distinct rows. `query.Count(column).WithDistinct()`, which [Aggregates](#aggregates) covers, counts the distinct non-NULL values of one column and decodes through `rasql.DecodeFrom[R]`. It is not a count of the rows `Distinct()` returns: it ignores NULL, which `DISTINCT` keeps as a value, and it takes only one expression rather than the several a distinct row de-duplicates on. The derived table or CTE that a portable distinct-row count needs is unsupported.

`DISTINCT ON`, PostgreSQL's syntax for keeping one row per group by explicit ordering, is out of scope: it needs its own dialect capability, since PostgreSQL is the only supported database that has it.

## Alias a table for a self-join

`As` returns the table under an alias with every column field rebound to it, so the alias qualifies the columns reached through the aliased value:

```go
// employees is a generated table with id and manager_id columns.
manager, err := employees.As("manager")
if err != nil {
	return err
}
rows, err := rasql.SelectFrom(employees).
	Join(rasql.InnerJoin(manager, query.Equal(employees.ManagerID, manager.ID))).
	Query(ctx, client)
```

`employees.ID` still renders as `"employees"."id"`, while `manager.ID` renders as `"manager"."id"`. `As` fails when the alias is not a valid identifier.

A table whose descriptor names a `Schema` (see [Qualify a table with a schema](02-schema.md#qualify-a-table-with-a-schema)) renders `"schema"."table"` in `FROM` and every write statement's target, and a column reached through it renders `"schema"."table"."column"`. An alias replaces a qualified table's whole name: once `events.As("e")` is taken, `e.ID` renders as `"e"."id"`, not `"audit"."e"."id"`, and this holds for every alias regardless of whether the aliased table was qualified.

### Every source of one statement needs its own name

A statement may carry two sources only when a server can tell their column references apart, and `Validate` refuses the statement when it cannot. A refused statement reports `table "…" is referred to as "…", which already refers to table "…"`, naming the two tables it could not separate. A source is referred to by its alias when it has one, and by its table name otherwise. Two sources therefore clash whenever:

- they share an alias, whatever the two tables are and whichever schemas they come from;
- one carries an alias that repeats another source's unaliased table name;
- they share a table name and at least one of them is unqualified, because a bare `"users"."id"` names an unqualified `users` and a qualified `tenant_a.users` equally.

Two unaliased tables of the same name in *different* schemas do not clash. Each renders its columns under its own `"schema"."table"` prefix, so `tenant_a.users` joined to `tenant_b.users` renders a statement whose every column reference names exactly one source. `TestSQLiteRefusesAmbiguousSources` runs that join against SQLite alongside each shape validation refuses.

rasql refuses a clash rather than inventing an alias to separate the two sources. An alias it chose would change the SQL you asked for and, through a projected column's result name, what a decoded row looks like, and it cannot tell which of the two sources a column reference you already wrote was meant to reach. Repair a refused statement by calling `As` on one of the two sources.

This check is new, and it is a behaviour change: two sources sharing one rendered name are now refused at validation, where an earlier release rendered them and sent the statement to the server. The refusal is what PostgreSQL and MySQL already do on their own — PostgreSQL reports SQLSTATE 42712 `duplicate_alias` and MySQL reports error 1066 `ER_NONUNIQ_TABLE`, each unconditionally — so no statement those two engines would have run is lost. SQLite is the exception: it accepts two sources under one name and fails only on a column reference it cannot resolve to exactly one of them, so `users AS u INNER JOIN orders AS u` is a shape SQLite alone would have executed and rasql now refuses on every dialect. The remedy is the same one a refused clash always takes: give the two sources distinct aliases with `As`.

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
	rows, err := rasql.DecodeFrom[orderSummary](users).
		Join(rasql.InnerJoin(orders, query.Equal(users.ID, orderUserID))).
		Project(query.Project(users.ID).As("user_id"), query.Project(users.Email)).
		Where(query.GreaterThan(total, query.Bind(20))).
		Order(query.Desc(total)).
		Query(ctx, client)
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
	// debug Queryer lets the example show the generated statement without a database.
	client, err := rasql.New(statementPrinter{}, dialect.PostgreSQL())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}

	// users is declared in query_example_tables_test.go with the shape rasqlgen
	// emits; an application would write store.Users() instead.
	count := 0
	rows, err := rasql.SelectFrom(users).WhereEqual(users.ID, 42).Query(context.Background(), client)
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

A debug `Queryer` may return `nil` rows after logging; `row.Scan` treats that as an empty result rather than an error. When only the SQL is wanted and no execution at all, `Build(d)` returns it from the dialect alone, with no `Executor`.

## Next

[Writing rows](04-writing.md) covers inserts, updates, and deletes, and [Static templates](05-templates.md) covers fixed SQL text with named binds.
