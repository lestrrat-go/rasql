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

// UsersTable embeds the typed table and adds one query.Column field per column.
// The query builders take those fields, so no column is named by a string.
type UsersTable struct {
	rasql.Table[UserRow]
	ID    query.Column
	Email query.Column
}

func newUsersTable(table rasql.Table[UserRow]) UsersTable {
	return UsersTable{
		Table: table,
		ID:    rasql.MustColumn(table, "id"),
		Email: rasql.MustColumn(table, "email"),
	}
}

// users keeps the generated row type and its column references together.
var users = newUsersTable(rasql.MustTable[UserRow](schema.Table{
	Name: "users",
	Columns: []schema.Column{
		{Name: "id", Type: schema.TypeInteger},
		{Name: "email", Type: schema.TypeText},
	},
	PrimaryKey: []string{"id"},
}))

// As returns the table under alias, with every column rebound to it.
func (t UsersTable) As(alias string) (UsersTable, error) {
	aliased, err := rasql.As(t.Table, alias)
	if err != nil {
		return UsersTable{}, err
	}
	return newUsersTable(aliased), nil
}
```
source: [examples/query_example_tables_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/query_example_tables_test.go)
<!-- END INCLUDE -->

Three things travel together here. `UserRow` is the Go type of one row, and its `rasql` tags name the column each field holds. The embedded `rasql.Table[UserRow]` binds that row type to a validated table description, so the compiler knows what a query against `users` returns. The `ID` and `Email` fields are the column references the query builders take.

Those fields are the reason a filter never spells a column as a string. `WhereEqual(users.ID, 42)` builds, while `WhereEqual(users.Emial, 42)` stops at the compiler with `users.Emial undefined (type UsersTable has no field or method Emial)`, and `WhereEqual("id", 42)` stops there too, because the parameter is a `query.Column` and not a name. [What the column fields catch](06-rasqlgen.md#what-the-column-fields-catch) shows what that covers and the three cases it does not.

[Schemas](02-schema.md) covers how to write these tables by hand, and [`rasqlgen`](06-rasqlgen.md) covers how to generate them.

## Create a client

A `rasql.Client` pairs a database handle with the dialect used to render SQL:

```go
client, err := rasql.New(database, dialect.SQLite())
```

`rasql.New` neither opens a connection nor starts a transaction. It accepts anything satisfying `rasql.Queryer`, which `*sql.DB` and `*sql.Tx` both do. Pass a `*sql.Tx` to run a group of statements in one transaction, or a custom implementation to inspect SQL without a database, as [Querying](03-querying.md) shows.

Pick the dialect that matches the database: `dialect.PostgreSQL()`, `dialect.MySQL()`, `dialect.SQLite()`, or `dialect.Spanner()`. The dialect decides how identifiers are quoted, how placeholders are numbered, how logical column types become DDL, and which syntax the renderer may use.

A `Client` is a value, not a handle to close. It is safe for concurrent use whenever the `Queryer` inside it is, so `*sql.DB` based clients can be shared across goroutines.

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
	// Create the schema described by the generated table descriptor.
	if err := rasql.Create(ctx, client, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	// Insert encodes UserRow's tagged fields as bound values.
	if _, err := rasql.Insert(ctx, client, users, UserRow{ID: 42, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// users is a typed table descriptor with the shape emitted by rasqlgen.
	user, err := rasql.SelectFrom(client, users).WhereEqual(users.ID, 42).One(ctx)
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

1. `rasql.Create` renders the table description as DDL and executes it, followed by any indexes. A real application usually creates tables through migrations instead, so this step is mostly a convenience for tests and examples.
2. `rasql.Insert` reads the tagged fields of `UserRow` and writes them as bound values. See [Writing rows](04-writing.md).
3. `rasql.SelectFrom(client, users)` starts a builder that already knows the result type. `WhereEqual` binds `42` as an argument rather than putting it into the SQL text.
4. `One` executes the statement and returns a single decoded `UserRow`, reporting an error when the result does not hold exactly one row.

The `database.SetMaxOpenConns(1)` call is a SQLite detail, not a `rasql` requirement. An in-memory SQLite database belongs to a single connection, so a pooled second connection would not see the created table.

## Handling errors

Query methods return the construction error before iteration begins, so an invalid statement fails at the call rather than midway through a loop. When ranging over results, the sequence yields rows first and at most one error after them, which is why every example checks the error inside the loop:

```go
rows, err := rasql.SelectFrom(client, users).Query(ctx)
if err != nil {
	// The statement could not be validated or rendered.
}
for user, err := range rows {
	if err != nil {
		// Execution or scanning failed. No further rows follow.
	}
}
```

## Next

[Schemas](02-schema.md) explains how to describe a table in Go, and how to read one back out of an existing database.
