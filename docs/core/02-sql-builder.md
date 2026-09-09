# The SQL builder

The `query` package represents portable SQL as validated immutable values. It does not need a database handle or Go row
type. The `render` package compiles those values for a dialect and returns `stmt.Statement`, which contains SQL text and
ordered bind arguments.

## Tables and relations

Create a table reference from a validated `schema.TableDef`:

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

`users.Column("email")` returns a source-bound column. `As` creates an alias. `query.Derived` and `query.CommonTable`
turn a result query into a reusable relation while preserving its declared result columns.

## Select

Use `query.NewSelect`, `query.NewGroupedSelect`, or `query.NewJoinedSelect` depending on which sources the initial
projection reads. Add clauses through immutable `With…` methods, as the compiled example above demonstrates.

The builder validates projection sources, joins, grouping, aggregate placement, ordering aliases, predicates, limits,
offsets, CTEs, compound queries, and locks. A failed refinement returns an error and leaves the original value unchanged.

## Expressions

Plain values passed to comparisons and assignments become bind arguments. Use `query.Bind` explicitly when an API needs
an expression value. The package provides:

- `Equal`, `NotEqual`, `LessThan`, `LessOrEqual`, `GreaterThan`, and `GreaterOrEqual`.
- `And`, `Or`, `Not`, `In`, `Between`, `Like`, `IsNull`, and `IsNotNull`.
- `Add`, `Subtract`, `Multiply`, `Divide`, `CastAs`, and searched `CASE`.
- `Count`, `CountAll`, `Sum`, `Average`, `Minimum`, and `Maximum`.
- `Coalesce`, `Lower`, `Upper`, `Abs`, and curated native functions.
- `Exists`, scalar subqueries, and correlated outer references where supported.

Use `query.Project(expression).As("name")` for an expression without a stable result name. Result aliases are required for
portable decoding of aggregates and computed expressions.

## Joins and reusable results

`query.InnerJoin`, `query.LeftJoin`, `query.RightJoin`, and `query.FullJoin` add source-bound joins. The selected dialect
may reject a join kind even when the portable model is structurally valid.

`query.ResultOf` attaches ordered result columns to a select, compound query, or native result. Use `query.Derived` for a
subquery relation and `query.CommonTable` for a CTE. Set operations require matching result widths and compatible types.

## Writes

`query.NewInsert`, `query.NewInsertRows`, `query.NewUpdate`, `query.NewDelete`, and `query.NewUpsert` build portable write
statements. Updates and deletes require a predicate or an explicit `AllowAll`. [Write statements](03-write-statements.md)
shows how to adapt them to `MutationPlan` for canonical execution.

## Render

Render a statement when a tool needs SQL without executing it. The compiled example above renders one statement for
PostgreSQL and MySQL and prints both the SQL and ordered arguments.

Rendering quotes identifiers, selects placeholders, and checks dialect capabilities. It never interpolates a bind value
into SQL text.

Application ORM code normally uses `rasql.Select` with a typed projection and an `Executor`. That path compiles the same
portable query model while also applying codecs, decoding rows, enforcing result cardinality, and recording lifecycle
events.

## Engine-specific SQL

Use `rasql.Native` or `rasql.NativeMutation` when the portable model does not represent required syntax. State the engine
explicitly. Placeholder rewriting alone cannot make native SQL portable.

## Next

[Querying](../02-querying.md) covers typed execution. [Schemas](01-schema.md) covers table definitions. [Named SQL](06-named-sql.md)
covers generated static templates.
