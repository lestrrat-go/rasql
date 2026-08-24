# Write statements

The `query` package builds every write statement, and `render` turns one into SQL text with its arguments in placeholder order. Neither step opens a database or asks for a Go row type, so a program that only has to produce SQL stops at the rendered statement. [Writing rows](../orm/04-writing.md) covers the typed path instead, which reads the columns off a Go row value.


<!-- INCLUDE(examples/query_render_write_example_test.go#render_write) -->
```go
func Example_query_render_write() {
	// A table description carries everything the DDL and the write
	// statements need. No row type and no database handle appear here.
	definition := schema.MustTableDef("accounts",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
	)
	accounts := query.MustTableRef(definition)
	id := accounts.Column("id")
	email := accounts.Column("email")

	// render.CreateTable turns the description into DDL for one dialect.
	ddl, err := render.CreateTable(dialect.PostgreSQL(), definition)
	if err != nil {
		fmt.Printf("failed to render the DDL: %s\n", err)
		return
	}
	fmt.Println(ddl.SQL())

	// query.NewInsert builds the statement; render.Insert turns it into SQL
	// text and the arguments that go with it. Each query.Set pairs a column
	// with the value written to it, so the two cannot fall out of step, and a
	// plain Go value is bound without a Bind wrapper.
	insert, err := query.NewInsert(accounts,
		query.Set(id, 1),
		query.Set(email, "ada@example.com"),
	)
	if err != nil {
		fmt.Printf("failed to build the insert: %s\n", err)
		return
	}
	rendered, err := render.Insert(dialect.PostgreSQL(), insert)
	if err != nil {
		fmt.Printf("failed to render the insert: %s\n", err)
		return
	}
	fmt.Println(rendered.SQL())
	fmt.Println(rendered.Args()...)

	// An update is the same two steps. WithWhere keeps it off every row.
	update, err := query.NewUpdate(accounts, query.Set(email, "grace@example.com"))
	if err != nil {
		fmt.Printf("failed to build the update: %s\n", err)
		return
	}
	update, err = update.WithWhere(query.Equal(id, 1))
	if err != nil {
		fmt.Printf("failed to add the predicate: %s\n", err)
		return
	}
	rendered, err = render.Update(dialect.PostgreSQL(), update)
	if err != nil {
		fmt.Printf("failed to render the update: %s\n", err)
		return
	}
	fmt.Println(rendered.SQL())
	fmt.Println(rendered.Args()...)

	// Output:
	// CREATE TABLE "accounts" ("id" BIGINT NOT NULL, "email" TEXT NOT NULL, PRIMARY KEY ("id"))
	// INSERT INTO "accounts" ("id", "email") VALUES ($1, $2)
	// 1 ada@example.com
	// UPDATE "accounts" SET "email" = $1 WHERE ("accounts"."id" = $2)
	// grace@example.com 1
}
```
source: [examples/query_render_write_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/query_render_write_example_test.go)
<!-- END INCLUDE -->

`render.CreateTable` takes the table description directly, so the DDL above comes out of a `schema.TableDef` alone. `rasql.CreateTable` in [Create a table](../orm/04-writing.md#create-a-table) renders the same DDL and executes it, which is why it asks for a `rasql.Table[T]` and an open database.

Inside an application, `rasql.Exec` runs any `query.WriteStatement`, which is what the `query` constructors produce: `NewInsert`, `NewInsertRows`, `NewUpdate`, `NewDelete`, and `NewUpsert`. Use them for conflict handling or SQL shapes not covered by the typed helpers. `Exec` rejects a statement carrying a `RETURNING` clause, because it discards result rows. Use `dynamic.QueryWrite` for one of those instead.

`NewUpdate` and `NewDelete` accept a missing predicate while a statement is being assembled, but rendering and execution reject that shape unless the intent is explicit. Call `statement.AllowAll()` and use the returned statement when every row should be changed. A predicate and `AllowAll` cannot be combined.

`NewInsert` pairs each column with its value through `query.Set`, the same call `NewUpdate` takes, so the two cannot fall out of step. The rendered column list follows the order the assignments were given in. Passing `query.Defaults()` on its own writes the database default for every column instead. `NewInsertRows` keeps a separate column list because an `INSERT` names its columns once and supplies every row against that one list.

`NewInsertRows` names its columns once, takes every row's values as one `[][]any`, binds each plain Go value the way `Set` does, and renders the rows as a single `INSERT` with several parenthesized `VALUES` groups. Rendering the rows as one statement does not make the insert atomic on its own: transaction scope, and whether a statement that fails partway rolls back the rows it already wrote, stay the caller's and the database's responsibility. A non-transactional MySQL table, for instance, keeps the rows written before the failure. Run the insert through the `rasql.DB` returned by `DB.Begin` when every row has to land or none of them. Bound parameters are still capped by the database (PostgreSQL and MySQL at 65535, SQLite's `modernc.org/sqlite` at 32766), so a very large row count needs chunking at the caller.

<!-- INCLUDE(examples/rasql_partial_update_example_test.go#partial_update) -->
```go
statement, err := query.NewUpdate(users.Ref(), query.Set(users.Email(), "ada@example.com"))
if err != nil {
	fmt.Printf("failed to build update: %s\n", err)
	return
}
statement, err = statement.WithWhere(query.LessThan(users.ID(), 100))
if err != nil {
	fmt.Printf("failed to filter update: %s\n", err)
	return
}
result, err := rasql.Exec(ctx, db, statement)
```
source: [examples/rasql_partial_update_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_partial_update_example_test.go)
<!-- END INCLUDE -->

Each `With…` method returns a new validated statement rather than changing the one it was called on, matching the immutable style of the select builders. `NewUpsert` accepts an explicit conflict target the same way. Check `dialect.CapabilityConflictTarget` before relying on it, since MySQL lacks it and rejects a statement that sets one.

A multi-row insert built with `NewInsertRows` carries `RETURNING` and conflict handling the same way a one-row insert does, with two caveats.

Row order in a `RETURNING` result is not guaranteed to match the order of the `VALUES` list: SQLite states outright that `RETURNING` output order is undefined, and PostgreSQL never promises it either. Project a column that identifies the row and match on it. Never correlate the result with the input rows by position.

That advice assumes each `VALUES` row leaves its own distinct row behind, which a repeated conflict key breaks. An upsert over two `VALUES` rows sharing one conflict key returns, on SQLite, one `RETURNING` row per `VALUES` row, both carrying the same identifying value, even though only one physical row exists afterwards. An identifying column tells you which row a result refers to, not how many results refer to the same row.

What a `NewUpsert` over a multi-row insert does when two `VALUES` rows carry the same conflict key follows from the conflict action the statement renders, not from the dialect alone, and `Validate` checks none of it.

With assignments, the statement renders `ON CONFLICT (...) DO UPDATE` on PostgreSQL and SQLite and `ON DUPLICATE KEY UPDATE` on MySQL, and only PostgreSQL fails. PostgreSQL raises a cardinality violation, `ON CONFLICT DO UPDATE command cannot affect row a second time`, because it refuses to touch one existing row twice within a single command. SQLite decides the upsert separately for each row, so the second row takes the `DO UPDATE` branch and overwrites what the first row inserted, without reporting an error. MySQL applies `ON DUPLICATE KEY UPDATE` to the conflicting row, so the second row likewise updates what the first one inserted.

Without assignments, meaning a `NewUpsert` that carries only a conflict target, the statement renders `ON CONFLICT (...) DO NOTHING` on PostgreSQL and SQLite, and MySQL rejects it at render time because it has neither `dialect.CapabilityConflictTarget` nor an assignment list to update with. Neither PostgreSQL nor SQLite reports an error in this form: the duplicate row is skipped. PostgreSQL's cardinality violation belongs to `ON CONFLICT DO UPDATE` alone.

Either keep conflict keys unique within one statement, or account for that update.

## Reading a `RETURNING` clause

`WithReturning` adds a `RETURNING` clause on dialects that support it. Check `dialect.CapabilityReturning` before relying on it, since MySQL does not. Once a statement carries one, `dynamic.QueryWrite` renders and runs it, returning the same rangeable `dynamic.Row` sequence a `SELECT` does, and the typed `rasql.QueryWriteAll[T]` and `rasql.QueryWriteOne[T]` decode that sequence the way `TypedSelectBuilder.All` and `.One` do. The typed pair stays in `rasql`, because they name a Go type rather than a column string.

<!-- INCLUDE(examples/rasql_returning_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/query"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// Example_rasql_returning reads the row a RETURNING clause produces, which
// rasql.Exec cannot do because it discards result rows.
func Example_rasql_returning() {
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

	// The insert names every column it has a value for. id is left to the
	// database and status to its column default, which is what this example
	// reads back.
	statement, err := query.NewInsert(users.Ref(),
		query.Set(users.Email(), "ada@example.com"),
		query.Set(users.FirstName(), "Ada"),
		query.Set(users.LastName(), "Lovelace"))
	if err != nil {
		fmt.Printf("failed to build insert: %s\n", err)
		return
	}

	// The RETURNING clause names all six columns rather than only the two the
	// database filled in. QueryWriteOne decodes into store.UsersRow, which maps
	// the whole users table, and it refuses a clause that omits a column of
	// that table: an omitted column would decode as a zero value with nothing
	// to say the database never sent it.
	statement, err = statement.WithReturning(users.ID(), users.Email(), users.Nickname(),
		users.Status(), users.FirstName(), users.LastName())
	if err != nil {
		fmt.Printf("failed to add RETURNING clause: %s\n", err)
		return
	}

	// SQL: INSERT INTO users (email, first_name, last_name) VALUES (?, ?, ?) RETURNING id, email, nickname, status, first_name, last_name (arguments: "ada@example.com", "Ada", "Lovelace")
	user, err := rasql.QueryWriteOne[store.UsersRow](ctx, db, statement)
	if err != nil {
		fmt.Printf("failed to query inserted user: %s\n", err)
		return
	}
	fmt.Printf("%d %q %q\n", user.ID, user.Email, user.Status)

	// Output:
	// 1 "ada@example.com" "pending"
}
```
source: [examples/rasql_returning_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_returning_example_test.go)
<!-- END INCLUDE -->

`QueryWriteOne` reports its row count through the same sentinels `One` does, described under [Select typed rows](../orm/03-typed-queries.md#select-typed-rows): `rasql.ErrNoRows` when `RETURNING` produced no rows, and `rasql.ErrMultipleRows` when it produced more than one.

Both typed terminals refuse a `RETURNING` clause that omits a column of the target table when `T` maps the whole table, because the omitted field would come back as a zero value with nothing to say so. A row type maps the whole table when it declares both `ScanRow` and `ScanDestinations`, which is the pair [rasqlgen](../orm/02-generated-store.md) writes for every row type it generates. A hand-written row type that maps only part of a table declares `ScanDestinations` alone, and these terminals then accept whatever projections it is given.

A fluent delete uses the same dynamic and typed terminals:

<!-- INCLUDE(examples/rasql_delete_returning_example_test.go#delete_returning_dynamic) -->
```go
builder := dynamic.DeleteFrom(users.Ref()).
	WhereEqual(users.ID(), 42).
	Returning(users.ID(), users.Email())

rows, err := builder.Query(ctx, db)
```
source: [examples/rasql_delete_returning_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_delete_returning_example_test.go)
<!-- END INCLUDE -->

`dynamic.DeleteFrom` returns `dynamic.Row` values from its own `Query`.
`rasql.QueryDeleteAll[T]` and `rasql.QueryDeleteOne[T]` decode the projections
into `T` and follow the same empty or multiple-row rules as `QueryWriteAll[T]`
and `QueryWriteOne[T]`. Use one terminal per builder:

<!-- INCLUDE(examples/rasql_delete_returning_example_test.go#delete_returning_typed) -->
```go
typed := rasql.DeleteFrom(users).
	WhereEqual(users.ID(), 43).
	Returning(users.ID(), users.Email(), users.Nickname(),
		users.Status(), users.FirstName(), users.LastName())

deleted, err := rasql.QueryDeleteOne[store.UsersRow](ctx, db, typed)
```
source: [examples/rasql_delete_returning_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_delete_returning_example_test.go)
<!-- END INCLUDE -->

A MySQL caller who needs a generated key skips `RETURNING` and reads `sql.Result.LastInsertId()` from `Exec` instead.

`DB.ExecRendered` runs a statement that is already rendered, which is how a compiled [static template](06-named-sql.md) is executed.


## Next

[The database handle](04-database.md) runs a rendered statement, installs hooks, and starts a transaction. [Writing rows](../orm/04-writing.md) writes a typed row without assembling a statement.
