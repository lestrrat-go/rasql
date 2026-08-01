# Schemas

A schema descriptor is the single description of a table that `rasql` uses everywhere. It generates DDL, validates dynamic queries, and tells the decoder which columns a result holds. Write it by hand, generate it with [`rasqlgen`](06-rasqlgen.md), or read it out of a live database.

## Describe a table in Go

<!-- INCLUDE(examples/schema_table_definition_example_test.go) -->
```go
package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/schema"
)

func Example_schema_table_definition() {
	// This example defines one reusable table descriptor in Go code.
	// Describe each database table once with schema.Table. The same descriptor
	// can later supply a reusable query.Table or generate DDL.
	table := schema.Table{
		// Name is the database table identifier.
		Name: "users",
		// Columns list each database column and its dialect-neutral logical type.
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		// PrimaryKey names columns from Columns that uniquely identify each row.
		PrimaryKey: []string{"id"},
	}
	// Validate the descriptor before it is used to create references or DDL.
	if err := table.Validate(); err != nil {
		fmt.Printf("failed to define table: %s\n", err)
		return
	}

	fmt.Printf("%s: %d columns\n", table.Name, len(table.Columns))

	// Output:
	// users: 2 columns
}
```
source: [examples/schema_table_definition_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/schema_table_definition_example_test.go)
<!-- END INCLUDE -->

`schema.Table` carries the table name, its columns, and its constraints:

| Field | Holds |
| --- | --- |
| `Name` | The table identifier. |
| `Columns` | Each column, in the order it is declared. |
| `PrimaryKey` | Column names from `Columns` that identify a row. |
| `UniqueConstraints` | Named or unnamed uniqueness requirements. |
| `Checks` | Check constraints. |
| `Indexes` | Secondary indexes. |
| `ForeignKeys` | References to other tables, with their update and delete actions. |

Call `Validate` before using a descriptor. It reports a `*schema.ValidationError` naming the part that is wrong, such as a primary key that lists a column the table does not declare. `MustTable` and `NewTable` validate as well, so a separate `Validate` call is only needed for a descriptor built at runtime that is not immediately turned into a table.

## Logical column types

A column's `Type` is a logical type, not a database type. The dialect maps it to real DDL, so the same descriptor works against every supported database.

| Logical type | Typical Go value |
| --- | --- |
| `schema.TypeBoolean` | `bool` |
| `schema.TypeInteger` | `int64` |
| `schema.TypeFloat` | `float64` |
| `schema.TypeText` | `string` |
| `schema.TypeBytes` | `[]byte` |
| `schema.TypeTime` | `time.Time` |
| `schema.TypeJSON` | `[]byte` or a type that marshals itself |
| `schema.TypeUUID` | `string` or a UUID type |

A column is also `Nullable` or not, and may carry a `Default` written as SQL text. Identifiers must be simple: `schema.ValidateIdentifier` accepts a leading letter or underscore followed by letters, digits, or underscores, and everything else is rejected rather than quoted around.

## Bind a row type to the table

A bare `schema.Table` describes the database. Pairing it with a Go type produces a `rasql.Table[T]`, which is what the typed API takes:

```go
type UserRow struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

users := rasql.MustTable[UserRow](definition)
```

Each field's `rasql` tag names the column it holds. `MustTable` panics on an invalid descriptor and suits generated or otherwise constant tables; `NewTable` returns the error instead, for descriptors assembled at runtime.

`users.Column(name)` returns a `query.Column` for a column of the table, and `users.QueryTable()` returns the underlying `query.Table`. The lower-level `query` package works in terms of those two types, and [Querying](03-querying.md) uses them for joins and projections. Tables written by [`rasqlgen`](06-rasqlgen.md) go further and expose one field per column, so `users.ID` needs no lookup and no error check. A hand-written table gets the same benefit by wrapping the descriptor the way the generator does, which [What the column fields catch](06-rasqlgen.md#what-the-column-fields-catch) shows in full.

## Read a table out of a database

`inspect` turns live database metadata back into a `schema.Table`, normalizing native column types into logical ones.

<!-- INCLUDE(examples/inspect_sqlite_table_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_inspect_sqlite_table() {
	// This example reads an existing SQLite table into a normalized schema.Table.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer database.Close()
	// Pretend this DDL already exists in an application-owned SQLite database.
	if _, err := database.ExecContext(ctx, "CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL, nickname TEXT)"); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	// The inspector uses the dialect to normalize native column metadata.
	inspector, err := inspect.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create SQLite inspector: %s\n", err)
		return
	}
	table, err := inspector.Table(ctx, "users")
	if err != nil {
		fmt.Printf("failed to inspect users table: %s\n", err)
		return
	}
	fmt.Printf("%s: %s, %s, %s\n", table.Name, table.Columns[0].Type, table.Columns[1].Type, table.Columns[2].Type)
	fmt.Println(table.PrimaryKey)

	// Output:
	// users: integer, text, text
	// [id]
}
```
source: [examples/inspect_sqlite_table_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/inspect_sqlite_table_example_test.go)
<!-- END INCLUDE -->

`inspect.New` takes the same kind of handle as `rasql.New` plus the dialect that describes the database being read. `Table` looks up one table by name. The result is an ordinary descriptor, so it can be validated, compared against a checked-in definition, or handed to the generator.

## Next

[Querying](03-querying.md) reads rows through these descriptors, or [Writing rows](04-writing.md) puts rows into them.
