# Getting started

This page runs one program end to end: describe a table, create it, write a row, and read it back as a Go value.

## Install

```sh
go get github.com/lestrrat-go/rasql
```

`rasql` needs Go 1.26 or newer. It executes through `database/sql`, so the application also imports a driver where it opens the connection. The examples use the pure-Go SQLite driver `modernc.org/sqlite`, which needs no cgo:

```sh
go get modernc.org/sqlite
```

## The table used throughout the documentation

Almost every example on these pages queries the same `users` table. It is defined once, in the shape `rasqlgen` emits for a generated table, and the other examples use it as if it came from generated source.

<!-- INCLUDE(examples/query_example_tables_test.go) -->
```go
package examples_test

import (
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

// UserRow, UsersTable, and users have the shape rasqlgen creates for a table
// definition. The other examples use them as if they came from generated source.
type UserRow struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

// UsersTable embeds the typed table and exposes one accessor method per
// column, so a mistyped column name fails to compile instead of failing at
// run time.
type UsersTable struct {
	rasql.Table[UserRow]
}

func (t UsersTable) ID() query.ColumnRef    { return rasql.ColumnOf(t.Table, "id") }
func (t UsersTable) Email() query.ColumnRef { return rasql.ColumnOf(t.Table, "email") }

// users keeps the generated row type and its table value together.
var users = UsersTable{rasql.MustTableOf[UserRow](schema.MustTableDef("users",
	schema.Integer("id"),
	schema.Text("email"),
	schema.PrimaryKey("id"),
))}

// As returns the table under alias.
func (t UsersTable) As(alias string) (UsersTable, error) {
	aliased, err := rasql.As(t.Table, alias)
	if err != nil {
		return UsersTable{}, err
	}
	return UsersTable{Table: aliased}, nil
}
```
source: [examples/query_example_tables_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/query_example_tables_test.go)
<!-- END INCLUDE -->

Three things travel together here. `UserRow` is the Go type of one row, and its `rasql` tags name the column each field holds. The embedded `rasql.Table[UserRow]` binds that row type to a validated table description, so the compiler knows what a query against `users` returns. The `ID` and `Email` methods are the column accessors the query builders take.

Those accessors are the reason a filter never spells a column as a string. `WhereEqual(users.ID(), 42)` builds, while `WhereEqual(users.Emial(), 42)` stops at the compiler with `users.Emial undefined (type UsersTable has no field or method Emial)`, and `WhereEqual("id", 42)` stops there too, because the parameter is a `query.ColumnRef` and not a name. [What the column accessors catch](06-rasqlgen.md#what-the-column-accessors-catch) shows what that covers and the three cases it does not.

[Schemas](02-schema.md) covers how to write these tables by hand, and [`rasql codegen`](06-rasqlgen.md) covers how to generate them.

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

`rasql.New` neither opens a connection nor starts a transaction. It accepts anything satisfying `rasql.Handle`, which `*sql.DB` and `*sql.Tx` both do, or a custom implementation to inspect SQL without a database, as [Querying](03-querying.md) shows. To start a transaction, call `Begin` on the resulting `DB` instead, which the [Transactions](04-writing.md#transactions) section covers.

Pick the dialect that matches the database: `dialect.PostgreSQL()`, `dialect.MySQL()`, or `dialect.SQLite()`. The dialect decides how identifiers are quoted, how placeholders are numbered, how logical column types become DDL, and which syntax the renderer may use.

A `DB` is a value, not a handle to close. It is safe for concurrent use whenever the `Handle` inside it is, so a `*sql.DB` based `DB` can be shared across goroutines.

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

The example moves through four steps.

1. `rasql.CreateTable` renders the table description as DDL and executes it, followed by any indexes. A real application usually creates tables through migrations instead, so this step is mostly a convenience for tests and examples.
2. `rasql.Insert` reads the tagged fields of `UserRow` and writes them as bound values. See [Writing rows](04-writing.md).
3. `rasql.SelectFrom(users)` starts a builder that already knows the result type. `WhereEqual` binds `42` as an argument rather than putting it into the SQL text.
4. `One` executes the statement and returns a single decoded `UserRow`, reporting `rasql.ErrNoRows` when the result holds no row and `rasql.ErrMultipleRows` when it holds more than one.

The `database.SetMaxOpenConns(1)` call is a SQLite detail, not a `rasql` requirement. An in-memory SQLite database belongs to a single connection, so a pooled second connection would not see the created table.

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

[Schemas](02-schema.md) explains how to describe a table in Go, and how to read one back out of an existing database.
