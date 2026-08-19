# Getting started

This page runs one program from end to end. The program describes a table, creates it, writes a row, and reads that row back as a Go value.

`rasql` comes in two layers. The core layer builds SQL text and runs it, and it needs no Go type for a row. The ORM layer sits on top and adds that row type, so a query hands back a decoded struct instead of raw columns. The program below uses the ORM layer, because that is what most applications reach for first. The last section, [A statement without a database](#a-statement-without-a-database), runs the core layer on its own.

## Install

```sh
go get github.com/lestrrat-go/rasql
```

`rasql` needs Go 1.26 or newer. It executes through `database/sql`, so the application also imports a driver where it opens the connection. The examples use the pure-Go SQLite driver `modernc.org/sqlite`, which needs no cgo:

```sh
go get modernc.org/sqlite
```

## The table used throughout the documentation

Almost every example on these pages queries the same `users` table. Three declarations make it up, and this section takes them one at a time.

A row type is an ordinary Go struct. Each field carries a `rasql` tag naming the column it holds.

<!-- INCLUDE(examples/query_example_tables_test.go#row_type) -->
```go
type UserRow struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}
```
source: [examples/query_example_tables_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/query_example_tables_test.go)
<!-- END INCLUDE -->

A table type embeds `rasql.Table[UserRow]` and adds one method per column. Those methods are the column accessors the query builders take.

<!-- INCLUDE(examples/query_example_tables_test.go#table_type) -->
```go
type UsersTable struct {
	rasql.Table[UserRow]
}

func (t UsersTable) ID() query.ColumnRef    { return rasql.ColumnOf(t.Table, "id") }
func (t UsersTable) Email() query.ColumnRef { return rasql.ColumnOf(t.Table, "email") }
```
source: [examples/query_example_tables_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/query_example_tables_test.go)
<!-- END INCLUDE -->

The table value ties the two together. `schema.MustTableDef` describes the table itself, and `rasql.MustTableOf` binds the row type to that description, so the compiler knows what a query against `users` returns.

<!-- INCLUDE(examples/query_example_tables_test.go#table_value) -->
```go
var users = UsersTable{rasql.MustTableOf[UserRow](schema.MustTableDef("users",
	schema.Integer("id"),
	schema.Text("email"),
	schema.PrimaryKey("id"),
))}
```
source: [examples/query_example_tables_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/query_example_tables_test.go)
<!-- END INCLUDE -->

`rasql codegen generate` writes all three from a live database, which [`rasql codegen`](orm/01-codegen.md) covers. These pages declare them by hand so that every example runs without a generator step. The same source file gives `UsersTable` an `As` method, which [Alias a table for a self-join](orm/03-typed-queries.md#alias-a-table-for-a-self-join) uses.

Those accessors are the reason a filter never spells a column as a string. `WhereEqual(users.ID(), 42)` builds, while `WhereEqual(users.Emial(), 42)` stops at the compiler with `users.Emial undefined (type UsersTable has no field or method Emial)`, and `WhereEqual("id", 42)` stops there too, because the parameter is a `query.ColumnRef` and not a name. [What the column accessors catch](orm/02-generated-store.md#what-the-column-accessors-catch) shows what that covers and the three cases it does not.

[Schemas](core/01-schema.md) covers how to write these tables by hand, and [`rasql codegen`](orm/01-codegen.md) covers how to generate them.

## Create a DB

A `rasql.DB` pairs a database handle with the dialect used to render SQL:

<!-- INCLUDE(examples/rasql_sqlite_query_example_test.go#new_db) -->
```go
db, err := rasql.New(database, dialect.SQLite())
if err != nil {
	fmt.Printf("failed to create rasql db: %s\n", err)
	return
}
```
source: [examples/rasql_sqlite_query_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_sqlite_query_example_test.go)
<!-- END INCLUDE -->

`rasql.New` wraps a handle the caller already opened. It accepts anything satisfying `rasql.Handle`, which `*sql.DB` and `*sql.Tx` both do, and it also accepts a custom implementation that inspects SQL without a database, as [Typed queries](orm/03-typed-queries.md#see-the-sql-without-a-database) shows. Call `Begin` on the resulting `DB` to start a transaction, which the [Transactions](core/04-database.md#transactions) section covers.

Pick the dialect that matches the database. The three are `dialect.PostgreSQL()`, `dialect.MySQL()`, and `dialect.SQLite()`. The dialect decides how identifiers are quoted, how placeholders are numbered, how logical column types become DDL, and which syntax the renderer may use.

A `DB` is a plain value, so nothing has to close it. It is safe for concurrent use whenever the `Handle` inside it is, so a `DB` built on a `*sql.DB` can be shared across goroutines.

## Run the first query

<!-- INCLUDE(examples/rasql_sqlite_query_example_test.go) -->
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

func Example_rasql_sqlite_query() {
	// This example creates, inserts, and reads one generated row with SQLite.
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
	// Create the schema described by the generated table descriptor.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	// Insert encodes UserRow's tagged fields as bound values.
	if _, err := rasql.Insert(ctx, db, users, UserRow{ID: 42, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// users is a typed table descriptor with the shape emitted by rasqlgen.
	// SQL: SELECT users.id, users.email FROM users WHERE users.id = ? (argument: 42)
	user, err := rasql.SelectFrom(users).WhereEqual(users.ID(), 42).One(ctx, db)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}

	fmt.Println(user.Email)

	// Output:
	// ada@example.com
}
```
source: [examples/rasql_sqlite_query_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_sqlite_query_example_test.go)
<!-- END INCLUDE -->

The program moves through four steps.

1. `rasql.CreateTable` renders the table description as DDL and executes it, followed by any indexes. A real application usually creates tables through migrations instead, so this step is mostly a convenience for tests and examples.
2. `rasql.Insert` reads the tagged fields of `UserRow` and writes them as bound values. See [Writing rows](orm/04-writing.md).
3. `rasql.SelectFrom(users)` starts a builder that already knows the result type. `WhereEqual` binds `42` as an argument rather than putting it into the SQL text.
4. `One` executes the statement and returns a single decoded `UserRow`, reporting `rasql.ErrNoRows` when the result holds no row and `rasql.ErrMultipleRows` when it holds more than one.

The `database.SetMaxOpenConns(1)` call is a SQLite detail rather than a `rasql` requirement. An in-memory SQLite database belongs to a single connection, so a pooled second connection would not see the created table.

## A statement without a database

The program above binds a Go row type to a table and runs it through a `rasql.DB`. The core layer stops one step earlier. `query` builds a statement from a table description, and `render` turns that statement into SQL text with its arguments. A test, a migration tool, or any code that hands the SQL to something else can stop right there. The example below describes its own `accounts` table, because this path needs no generated code and no row type.

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
	id, err := accounts.Column("id")
	if err != nil {
		fmt.Printf("failed to reference the id column: %s\n", err)
		return
	}
	email, err := accounts.Column("email")
	if err != nil {
		fmt.Printf("failed to reference the email column: %s\n", err)
		return
	}

	// query.NewSelect validates the statement as it builds it.
	statement, err := query.NewSelect(accounts, query.Project(id), query.Project(email))
	if err != nil {
		fmt.Printf("failed to build the select: %s\n", err)
		return
	}
	statement, err = statement.WithWhere(query.Equal(email, query.Bind("ada@example.com")))
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

[Querying](02-querying.md) compares the two builders and says which one a task calls for.

## Handling errors

Query methods return the construction error before iteration begins, so an invalid statement fails at the call rather than midway through a loop. When ranging over results, the sequence yields rows first and at most one error after them, which is why every example checks the error inside the loop:

<!-- INCLUDE(examples/rasql_query_errors_example_test.go#query_errors) -->
```go
rows, err := rasql.SelectFrom(users).Query(ctx, db)
if err != nil {
	// The statement could not be validated or rendered.
	fmt.Printf("failed to query users: %s\n", err)
	return
}
for user, err := range rows {
	if err != nil {
		// Execution or scanning failed. No further rows follow.
		fmt.Printf("failed to read user: %s\n", err)
		return
	}
	fmt.Println(user.Email)
}
```
source: [examples/rasql_query_errors_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_query_errors_example_test.go)
<!-- END INCLUDE -->

## Next

[Schemas](core/01-schema.md) explains how to describe a table in Go, and how to read one back out of an existing database. [Querying](02-querying.md) compares the two builders that read rows through those descriptions.
