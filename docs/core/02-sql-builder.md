# The SQL builder

`rasql` builds a statement in one of two ways. The ORM binds a table to a Go row type, so a query returns decoded rows, the compiler checks each column, and the call that executes needs a `rasql.DB`. [Typed queries](../orm/03-typed-queries.md) covers that layer. The raw SQL builder, which this page covers, stops at the SQL text and its arguments and leaves the running to the caller.

The `query` package builds the statement and validates it, and the `render` package turns that statement into SQL text with its arguments in placeholder order. Both packages import `schema` and `dialect` and nothing else of `rasql`, so a statement is built and rendered with no database handle and no Go row type in sight.

A statement is dialect-neutral until it renders. The same `query.Select` becomes PostgreSQL, MySQL, or SQLite text depending on the `dialect.Dialect` passed to `render`, and a plain Go value stays an argument in every one of them.

## Build and render a statement

<!-- INCLUDE(examples/query_render_select_example_test.go#render_select) -->
```go
func Example_query_render_select() {
	// The query and render packages need no database handle and no Go row
	// type. A table description is the only input.
	accounts := query.MustTableRef(schema.MustTableDef("accounts",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
	))
	id := accounts.Column("id")
	email := accounts.Column("email")

	// query.NewSelect validates the statement as it builds it.
	statement, err := query.NewSelect(accounts, id, email)
	if err != nil {
		fmt.Printf("failed to build the select: %s\n", err)
		return
	}
	statement, err = statement.WithWhere(query.Equal(email, "ada@example.com"))
	if err != nil {
		fmt.Printf("failed to add the predicate: %s\n", err)
		return
	}

	// One statement renders for whichever dialect it is given. The value
	// stays an argument in both, so it never becomes SQL text.
	for _, d := range []dialect.Dialect{dialect.PostgreSQL(), dialect.MySQL()} {
		rendered, err := render.Select(d, statement)
		if err != nil {
			fmt.Printf("failed to render the select: %s\n", err)
			return
		}
		fmt.Println(rendered.SQL())
		fmt.Println(rendered.Args()...)
	}

	// Output:
	// SELECT "accounts"."id", "accounts"."email" FROM "accounts" WHERE ("accounts"."email" = $1)
	// ada@example.com
	// SELECT `accounts`.`id`, `accounts`.`email` FROM `accounts` WHERE (`accounts`.`email` = ?)
	// ada@example.com
}
```
source: [examples/query_render_select_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/query_render_select_example_test.go)
<!-- END INCLUDE -->

## Extend rendering with a compiler

An optional `dialect.CompilerProvider` extends rendering for a custom dialect. Its compiler can emit a complete pagination
clause or handle an external `query.Expression` through the renderer's identifier, argument, and child-expression emitter.
The built-in dialects keep their existing SQL when no compiler is installed.

<!-- INCLUDE(examples/query_custom_compiler_example_test.go) -->
```go
package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/query"
)

type compilerExampleDialect struct {
	dialect.Dialect
}

func (compilerExampleDialect) Compiler() dialect.Compiler { return compilerExample{} }

type compilerExample struct{}

type containsExample struct {
	column query.Expression
	value  any
}

func (containsExample) ExpressionNode()              {}
func (containsExample) CustomExpressionName() string { return "contains" }

func (compilerExample) CompileExpression(emitter dialect.Emitter, expression query.Expression) (bool, error) {
	contains, ok := expression.(containsExample)
	if !ok {
		return false, nil
	}
	emitter.WriteSQL("CONTAINS(")
	if err := emitter.Expression(contains.column); err != nil {
		return true, err
	}
	emitter.WriteSQL(", ")
	if err := emitter.Argument(contains.value); err != nil {
		return true, err
	}
	emitter.WriteSQL(")")
	return true, nil
}

func (compilerExample) CompilePagination(emitter dialect.Emitter, pagination dialect.Pagination) error {
	if pagination.HasOffset {
		return fmt.Errorf("offset is unsupported")
	}
	if pagination.HasLimit {
		emitter.WriteSQL(" FETCH FIRST ")
		if err := emitter.Argument(pagination.Limit); err != nil {
			return err
		}
		emitter.WriteSQL(" ROWS ONLY")
	}
	return nil
}

func Example_customCompiler() {
	users := store.Users()
	builder := rasql.DecodeFromRef[struct{}](users.Ref()).
		Project(query.Project(containsExample{column: users.Email(), value: "@example.com"})).
		Limit(3)
	statement, err := builder.Build(compilerExampleDialect{Dialect: dialect.SQLite()})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(statement.SQL())
	fmt.Println(statement.Args())
	// Output:
	// SELECT CONTAINS("users"."email", ?) FROM "users" FETCH FIRST ? ROWS ONLY
	// [@example.com 3]
}
```
source: [examples/query_custom_compiler_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/query_custom_compiler_example_test.go)
<!-- END INCLUDE -->

#### Escape hatch for an uncurated function

`query.Func(name, arguments…)` calls the SQL function named `name` on `arguments`, and `name` is any string rather than one of the `query.Function…` constants. It reaches a function this package does not curate without waiting for a change to rasql, such as `query.Func("jsonb_path_query", doc, "$.a")`.

`Func` guarantees only that `name` is a legal SQL identifier before it reaches SQL text, reusing the same identifier rule `schema.ValidateIdentifier` applies to a column or table name. Validation reports a `query.ValidationError` naming the offending input when it is not. `Func` guarantees nothing about `name` beyond that: whether the named function exists on the target database, what arguments it takes, what it does, and whether it renders identically across PostgreSQL, MySQL, and SQLite are entirely the caller's responsibility. Every argument still goes through the ordinary expression nodes, so a plain value still travels as a placeholder rather than as SQL text. Only the function's own name reaches SQL unescaped, and only once validation has confirmed it is a legal identifier. A call built with `Func` is always treated as scalar and reaches every clause a curated scalar call does, with no arity check and no aggregate placement rule, even when `name` happens to match a curated aggregate such as `"SUM"`. `function.Aggregates()` reports `false` for it for the same reason. Prefer `Call` with a `query.Function…` constant, or `Coalesce`, `Lower`, `Upper`, or `Abs`, whenever the function is one of them.

`query.Function.WithDistinct()` is the one rule a `Func` call does not inherit from a curated scalar call: it is carried through to the rendered SQL rather than refused, so `query.Func("group_concat", tag).WithDistinct()` renders as `group_concat(DISTINCT tag)`. rasql does not know whether the named function aggregates, and `DISTINCT` is the only way to reach an aggregate it does not curate. Whether the target database accepts the call is the caller's responsibility, exactly as everything else about a `Func` name is.

### Subqueries

`query.Subquery` is a `SELECT` statement used as an expression: `query.Scalar(statement)` uses it as a single value, and `query.InSelect`/`query.NotInSelect` use it as the
right-hand side of a membership test. In those three forms, `statement` must project exactly one expression — validation reports the count when it does not — because
`x > (SELECT a, b …)` is as invalid as `x IN (SELECT a, b …)`.

`query.Exists(statement)` and `query.NotExists(statement)` test whether the statement returns any row at all. They read no value from it, only whether a row arrived, so
`statement` may project any number of expressions and the projection is arbitrary. Project a column of the subquery's own table, such as its primary key. `SELECT 1` is
the conventional body in hand-written SQL and `query.Project(query.Bind(1))` is how it is written here, but a bound value is a placeholder rather than the literal `1`:
PostgreSQL has nothing to infer that placeholder's type from in a projection standing on its own, types it as `text`, and the pgx driver then refuses to encode a Go `int`
as text. MySQL and SQLite both run the same statement, so a bound integer there builds a query that works on two engines and fails on the third.
`query.Project(query.Bind("1"))` runs on all three; a column costs no parameter at all.

A subquery is legal in the projections, `JOIN ON` conditions, `WHERE` clause, `GROUP BY` clause, `HAVING` clause, and `ORDER BY` clause of a
`SELECT` statement, in the `WHERE` clause of a `DELETE` statement, and in the `WHERE` clause and `SET` assignments of an `UPDATE` statement.
Every other clause of a write statement refuses one, the same way those clauses refuse an aggregate: an `INSERT`'s `VALUES` rows, an upsert's
conflict-update assignments, and the `RETURNING` clause of any of them. A subquery may nest inside another subquery to any depth.

#### Correlated subqueries

A subquery may read a column of the statement enclosing it, alongside the columns of its own `FROM` and joins. Reading one correlates the subquery: the database evaluates
it once per enclosing row rather than once for the whole statement, and the row it reads is the enclosing row being tested. Correlation is what `EXISTS` is for — an
`EXISTS` reading only its own tables asks nothing about the row being tested and merely reports whether a table is non-empty — and it reaches every other form too, so a
scalar subquery counting one user's orders beside that user is the same mechanism.

Call `query.Select.WithCorrelation(tables…)` to name the enclosing tables the statement reads, before the clause that reads them. Every builder method validates the copy
it returns, and a statement under construction has no enclosing statement to ask, so `WithWhere` refuses a predicate naming a table this statement has not been told
about. That is the same ordering `query.NewJoinedSelect` documents for a join a projection reads. A statement between the reader and the table declares it too, since
validating that middle statement on its own has nothing else saying a third statement is coming.

The enclosing statement may be a `SELECT`, a `DELETE`, or an `UPDATE`. Each one has the row a correlated subquery reads: a result row for a `SELECT`, and the row being
written for the other two. `DELETE FROM users WHERE EXISTS (SELECT orders.id FROM orders WHERE orders.user_id = users.id)` is the shape that enables, and PostgreSQL 17,
MySQL 8.4 and SQLite all run it, in a `DELETE`'s `WHERE` clause, an `UPDATE`'s `WHERE` clause, and an `UPDATE`'s `SET` assignment value alike.

Correlation needs no dialect capability of its own. MySQL's error 1093 is about the subquery's own `FROM` naming the write target, not about a column reference reaching
out to it, so `dialect.CapabilityWriteSubqueryTarget` still draws the line in the same place: a correlated subquery reading another table renders for MySQL, and one
selecting from the write target is refused for MySQL and rendered for PostgreSQL and SQLite, exactly as an uncorrelated one is.

The declaration is checked at both ends. A table named in it that the enclosing statement does not carry is refused when the subquery is nested, and `render.Select`
refuses a statement that declares a correlation and is rendered on its own, since a subquery position is the only place the enclosing row exists at all. `query.Validate`
accepts that statement, because it is a consistent model of a subquery; this is the same division the `MATCH` operator already follows, where validation accepts the shape
and rendering refuses the dialects that cannot run it.

The tables named in the declaration are exactly what the subquery gets, and the enclosing statement's other tables stay out of its scope. That is what keeps a subquery
selecting from a table the enclosing statement also selects from legal, which is the shape a `DELETE`'s `IN (SELECT … FROM the target …)` takes: nothing in that subquery
can reach the enclosing copy, so no column reference in it is ambiguous, and a server resolves each one to the subquery's own table exactly as rasql validated it.

Declaring a correlation with a table the subquery also selects from is what makes both copies reachable at once, and that pair is refused rather than resolved. SQL itself
answers such a reference from the innermost statement that has a match, and answers it silently: the enclosing row the declaration asked for is then unreadable, with
nothing in the Go code saying so. rasql refuses the pair and names the alias that separates the two scopes, which is what it already does for two tables of
one statement a server could not tell apart. Alias one of the two tables with `TableRef.As` and the statement says which table each reference meant.

An aggregate inside a subquery belongs to that subquery's own clause and its own grouping, never to the enclosing statement's, so a subquery that aggregates may sit
beside a bare column in an ungrouped statement, and a column the subquery reads never makes the enclosing statement look like it reads one. A correlated column read
alongside an aggregate is left to the database, exactly as a column projected outside an aggregate that is not among the grouping keys already is.

[Filter with a subquery](../orm/03-typed-queries.md#filter-with-a-subquery) builds an uncorrelated `InSelect` and a `Scalar` over an alias of the enclosing statement's
table, and [Filter with EXISTS](../orm/03-typed-queries.md#filter-with-exists) builds the correlated `EXISTS` and `NOT EXISTS` pair against a database.

`query.InSelect` costs no argument per candidate, unlike `query.In`, so a set of any size fits within the dialect's parameter limit. The arguments a subquery binds join
the enclosing statement's argument list at the position the subquery occupies, so placeholder numbering stays correct in every dialect. MySQL refuses a `LIMIT` or an
`OFFSET` on the statement given to `InSelect` or `NotInSelect` — error 1235 — so rendering for MySQL reports an error instead of sending SQL the server would reject.
PostgreSQL and SQLite accept it. That restriction does not apply to `Scalar`, which MySQL accepts with a `LIMIT`, nor to `Exists` and `NotExists`: error 1235 names a
`LIMIT` in an `IN`/`ALL`/`ANY`/`SOME` subquery, and MySQL runs `EXISTS (SELECT … LIMIT 1)`.

MySQL also refuses a `DELETE` or an `UPDATE` whose subquery reads the table the statement writes to, answering error 1093, `You can't
specify target table 't' for update in FROM clause`. It answers that however the subquery reaches the table: named in the subquery's own
`FROM`, named there under an alias, joined to, or read by a subquery nested inside it. An `UPDATE` earns 1093 from either of its clauses,
so a subquery reading the target from the `WHERE` clause and one reading it from a `SET` assignment's value are both refused. PostgreSQL
and SQLite run every one of those shapes and hold `dialect.CapabilityWriteSubqueryTarget`, which MySQL does not, so `render.Delete` and
`render.Update` return a `*render.SubqueryReadsWriteTargetError` for MySQL instead of sending SQL the server would reject. Run the `SELECT`
as a statement of its own and pass the rows it returns to `query.In`, or point the subquery at a table other than the target.

Two spellings of one table count as the same table here, so a bare `users` and a schema-qualified `app.users` are refused together. An
unqualified name resolves against whatever schema the connection is currently using, which rasql does not know, and MySQL answered 1093 to
that mixed spelling too. Only a pair that states two different schemas is treated as two tables.

### Full-text search (SQLite FTS5)

SQLite's FTS5 module treats a virtual table's own name as an implicit column: it is the left operand of `MATCH` and the first argument to `bm25` and FTS5's other auxiliary functions. `query.TableIdentifier` is the expression node that renders a `query.TableRef` this way, as a bare quoted identifier with no schema and no column; `table.Identifier()` builds one directly, and `query.Match` and `query.BM25` below build one internally so a caller reaching for the common shapes never has to.

| Constructor | Renders |
| --- | --- |
| `query.Match(table, expr)` | `"table" MATCH expr`, filtering every indexed column of table's row at once. |
| `query.Compare(table.Column(name), query.OperatorMatch, expr)` | `"table"."column" MATCH expr`, filtering one column instead of the whole row — FTS5 supports this shape too. |
| `query.BM25(table, weights…)` | `BM25("table", weights…)`, FTS5's ranking function; a query with no weights at all leaves every column weighed 1.0. |
| `table.Identifier()` | The bare `query.TableIdentifier` `Match` and `BM25` build internally, for a call this package does not curate. |

`query.OperatorMatch` is only valid on a dialect that grants `dialect.CapabilityMatchOperator`, which today is SQLite alone: MATCH is answered by a virtual table's own module rather than by SQL itself, MySQL's own full-text search is a different syntax entirely — `MATCH (cols) AGAINST (expr)`, not a binary operator — and PostgreSQL has neither. `render.Select` and `render.Update` refuse a statement built with `query.Match` or `query.Compare(…, query.OperatorMatch, …)` for any other dialect, returning a `*render.UnsupportedMatchOperatorError` rather than sending SQL the server would reject.

`FunctionBM25` is curated, unlike most FTS5 auxiliary functions, because its first argument has a shape validation can check that SQL itself cannot: a `query.ColumnRef` or a bound value in that position still looks like a plausible argument once rendered, so `query.Call(query.FunctionBM25, …)` and `query.BM25` both refuse anything there but a `query.TableIdentifier`. FTS5's `rank` needs no such curation: it is an ordinary hidden column SQLite adds to every FTS5 table, read the same way any other column is, through `table.Column("rank")`.

[SQLite virtual tables](08-inspection-facts.md#sqlite-virtual-tables) covers `TableDef.VirtualTableModule` and the hidden columns FTS5 and other modules declare; `render.CreateTable` does not build `CREATE VIRTUAL TABLE` DDL, so a caller creates the FTS5 table itself before building statements against it here.

<!-- INCLUDE(examples/query_render_select_match_example_test.go#render_select_match) -->
```go
func Example_query_render_select_match() {
	// query.TableRef carries no Go row type here, exactly as in
	// query_render_select_example_test.go, so every column is still named
	// by string rather than through a generated accessor.
	notesFTS := query.MustTableRef(schema.TableDef{
		Name: "notes_fts",
		Columns: []schema.ColumnDef{
			{Name: "title", Type: schema.TextType{}},
			{Name: "body", Type: schema.TextType{}},
		},
	})

	// query.BM25 takes the table's own identifier as its first argument,
	// never a plain string, so "notes_fts" reaches SQL through
	// Dialect.QuoteIdentifier rather than as a bound parameter. The two
	// weights that follow favor a match in title over one in body.
	score := query.BM25(notesFTS, 2.0, 1.0)
	statement, err := query.NewSelect(notesFTS, notesFTS.Column("title"), score.As("score"))
	if err != nil {
		fmt.Printf("failed to build the select: %s\n", err)
		return
	}

	// query.Match builds the same shape for MATCH: the table's own
	// identifier on the left, so it filters every indexed column at once.
	statement, err = statement.WithWhere(query.Match(notesFTS, "dinosaur"))
	if err != nil {
		fmt.Printf("failed to add the predicate: %s\n", err)
		return
	}
	// rasql orders by an expression, not by a projection's result name, so
	// the ordering repeats the same BM25 call rather than naming "score".
	statement, err = statement.WithOrder(query.Asc(score))
	if err != nil {
		fmt.Printf("failed to add the ordering: %s\n", err)
		return
	}

	// MATCH is SQLite-only: render.Select refuses it for a dialect that
	// lacks dialect.CapabilityMatchOperator rather than send SQL neither
	// PostgreSQL nor MySQL understands.
	rendered, err := render.Select(dialect.SQLite(), statement)
	if err != nil {
		fmt.Printf("failed to render the select: %s\n", err)
		return
	}
	fmt.Println(rendered.SQL())
	fmt.Println(rendered.Args()...)

	if _, err := render.Select(dialect.PostgreSQL(), statement); err != nil {
		fmt.Println(err)
	}

	// Output:
	// SELECT "notes_fts"."title", BM25("notes_fts", ?, ?) AS "score" FROM "notes_fts" WHERE ("notes_fts" MATCH ?) ORDER BY BM25("notes_fts", ?, ?)
	// 2 1 dinosaur 2 1
	// render postgresql: the postgresql dialect cannot express MATCH: it has no full-text search operator
}
```
source: [examples/query_render_select_match_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/query_render_select_match_example_test.go)
<!-- END INCLUDE -->

### Projections, joins, and ordering

| Constructor | Produces |
| --- | --- |
| `column` | A column projected under its own name; a `query.ColumnRef` is a projection already. |
| `column.As(alias)` | The same column under a result name. |
| `query.Count(expression)` and the other function constructors | A function call projected under whatever name the database picks; a `query.Function` is a projection already. |
| `query.Lower(expression).As(alias)` | The same call under a result name. |
| `query.Project(expression)` | A projected expression, for anything that is neither a column nor a function call. |
| `query.Project(expression).As(alias)` | The same projection under a result name. |
| `rasql.InnerJoin(table, on)` | An inner join on a typed table with its condition. |
| `rasql.LeftJoin(table, on)` | A left outer join on a typed table with its condition. |
| `query.InnerJoin(queryTable, on)`, `query.LeftJoin(queryTable, on)` | The same joins on a `query.TableRef`, for dynamic code. |
| `query.Asc(expression)`, `query.Desc(expression)` | Ordering for `Order`. |
| `query.AscResult(projection)`, `query.DescResult(projection)` | Ordering for `Order` by a projection's already-computed result instead of its expression. |

#### Order by a projection's result name

`query.AscResult(projection)` and `query.DescResult(projection)` order by `projection`'s result name — the name `As` gave it, or, for a column selected without a wrapper, the column's own name — rather than by the expression behind it. `projection` is the same value passed to `Project` (or the same `query.ColumnRef` projected on its own), so the name is written once: this is what `SELECT … AS alias … ORDER BY alias` means, and the ordering reads the projection's already-computed result instead of recomputing the expression, so a long function call or a scalar subquery is written once instead of twice, and renaming its alias with `As` cannot drift the two apart the way repeating the name as a second string could.

`projection` is not a `query.Expression`, which is deliberate. A result name resolves against the statement's own projections rather than against its tables, and SQL admits it in exactly one position: alone, as a whole `ORDER BY` term. PostgreSQL refuses `ORDER BY alias || 'x'` with "column does not exist" while MySQL and SQLite run it, so an expression node carrying a result name would build statements that work on two dialects and fail on the third. Keeping it a kind of `Order` instead means `WithWhere`, `WithGroupBy`, `WithHaving`, and a join condition all refuse it at compile time, since each of those takes an `Expression`.

Validation resolves `projection` to the result name it reports and refuses one that reports no name at all: an unaliased function call or `query.Project` reports whatever name the database picks for it, which is not portable, so give such a projection an alias with `As` before ordering by it, or call `query.Asc` on the expression instead. It also refuses a name no projection of the statement reports, so the ordering cannot name a result the statement never produces, and a name more than one projection reports: PostgreSQL answers that shape with "ORDER BY is ambiguous" and MySQL with error 1052, while SQLite silently sorts by whichever projection was aliased, so refusing it here is what keeps one statement meaning one thing on all three. Membership and ambiguity are both judged by that resolved name, not by comparing `projection` against the statement's projections directly, since a projection built by `query.Project` can hold an expression that is not safe to compare that way — a caller whose alias happens to collide with an unaliased call's own database-chosen name, such as `SELECT count(*), max(id) AS count … ORDER BY count`, is refused by PostgreSQL and accepted by MySQL, and rasql leaves that residual case to the database rather than modeling a name it cannot know.

There is no ordinal form, `ORDER BY 2`. It works on every supported engine, but an ordinal is a position into the select list: insert a projection ahead of it and the statement keeps compiling, keeps validating, keeps running, and silently sorts by a different column. Give the projection an alias with `As` and order by that name instead.

<!-- INCLUDE(examples/rasql_order_by_alias_example_test.go#order_by_alias) -->
```go
// Example_rasql_order_by_alias binds a projection to a variable once and
// passes that same variable to both Project and Order, so the ORDER BY reads
// the projection's already-computed result instead of repeating its
// expression, and renaming its alias can never drift the two apart the way
// writing the alias out as a second string could. displayName falls back
// from nickname to email with COALESCE.
func Example_rasql_order_by_alias() {
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
	users := store.Users()
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	nick := "Ada"
	for _, user := range []store.UsersRow{
		{ID: 1, Email: "ada@example.com", Nickname: &nick},
		{ID: 2, Email: "bob@example.com", Nickname: nil},
	} {
		if _, err := rasql.Insert(ctx, db, users, user); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// A local result type holds the decoded id and the aliased display name.
	// DisplayName has no rasql tag, so it maps to the alias by snake-casing
	// the field name to display_name.
	type userDisplayName struct {
		ID          int64
		DisplayName string
	}

	// displayName is written once and used in both Project and Order below.
	// SQL: SELECT users.id, COALESCE(users.nickname, users.email) AS display_name FROM users ORDER BY display_name DESC
	displayName := query.Coalesce(users.Nickname(), users.Email()).As("display_name")
	rows, err := rasql.DecodeFrom[userDisplayName](users).
		Project(users.ID(), displayName).
		Order(query.DescResult(displayName)).
		Query(ctx, db)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	for user, err := range rows {
		if err != nil {
			fmt.Printf("failed to read user: %s\n", err)
			return
		}
		fmt.Println(user.ID, user.DisplayName)
	}

	// A second builder orders by a projection whose result name two
	// projections report: id is projected without a wrapper, and nickname is
	// separately aliased id. rasql refuses this in Go rather than letting it
	// reach a server, since PostgreSQL and MySQL both call it ambiguous and
	// SQLite would otherwise resolve it silently.
	nicknameAsID := users.Nickname().As("id")
	_, err = rasql.DecodeFrom[userDisplayName](users).
		Project(users.ID(), nicknameAsID).
		Order(query.AscResult(nicknameAsID)).
		Build(dialect.SQLite())
	if err != nil {
		fmt.Println(err)
	}

	// Output:
	// 2 bob@example.com
	// 1 Ada
	// query: order_by[0]: orders by the result name "id", which 2 projections report, so the ordering is ambiguous; give one of them a distinct alias
}
```
source: [examples/rasql_order_by_alias_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_order_by_alias_example_test.go)
<!-- END INCLUDE -->

## Next

[Typed queries](../orm/03-typed-queries.md) adds the generated table and the row type, and works through joins, grouping, and custom result shapes. [Named SQL](06-named-sql.md) covers hand-written SQL text for the syntax this builder does not model.
