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

Call `Validate` before using a descriptor. It reports a `*schema.ValidationError` naming the part that is wrong, such as a primary key that lists a column the table does not declare. Non-empty names given to `UniqueConstraints`, `Checks`, and `ForeignKeys` must be unique across all three, since a dialect renders them together into one `CREATE TABLE` constraint list. `MustTable` and `NewTable` validate as well, so a separate `Validate` call is only needed for a descriptor built at runtime that is not immediately turned into a table.

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
| `schema.TypeDecimal` | `string` |

A column is also `Nullable` or not, and may carry a `Default` written as SQL text. Identifiers must be simple: `schema.ValidateIdentifier` accepts a leading letter or underscore followed by letters, digits, or underscores, and everything else is rejected rather than quoted around.

`schema.TypeDecimal` is an exact decimal, for money, quantities, and any other value a binary floating-point `TypeFloat` would round. A decimal column must set `Precision` (the total number of significant digits, at least 1) and `Scale` (the number of those digits right of the decimal point, no more than `Precision`); `Table.Validate` rejects a decimal column that omits either and rejects `Precision` or `Scale` on any other logical type. Each dialect renders `Precision`/`Scale` into its own DDL: PostgreSQL and MySQL render `NUMERIC(p,s)` and `DECIMAL(p,s)`, each exact and each enforcing its own maximum precision and scale. SQLite has no exact decimal storage class, so it renders `TEXT` instead: the column round-trips its digits exactly, decoding to a Go `string` on every dialect, but a SQLite decimal column compares and orders lexicographically rather than numerically, since it is stored as text rather than a number. A caller that wants a real decimal type in Go, rather than a `string`, can write its own row struct with a field implementing `sql.Scanner` and `driver.Valuer`; `row.Assign` checks for that interface before every built-in conversion, so the raw driver value reaches it unchanged.

<!-- INCLUDE(examples/schema_decimal_column_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// invoiceRow maps the one schema.TypeDecimal column this example declares.
// The column decodes into a Go string, so the exact digits inserted are the
// exact digits read back.
type invoiceRow struct {
	ID     int64  `rasql:"id"`
	Amount string `rasql:"amount"`
}

func Example_schema_decimal_column() {
	// This example declares a schema.TypeDecimal column, creates its table in
	// SQLite, and shows that the inserted string round-trips unchanged.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer database.Close()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}

	// A TypeDecimal column must state Precision and Scale; Table.Validate
	// rejects a decimal column that omits either.
	invoices := rasql.MustTable[invoiceRow](schema.Table{
		Name: "invoices",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "amount", Type: schema.TypeDecimal, Precision: 19, Scale: 4},
		},
		PrimaryKey: []string{"id"},
	})
	// SQLite has no exact decimal storage class, so the dialect declares this
	// column TEXT rather than NUMERIC(19,4), which would round through REAL.
	if err := rasql.Create(ctx, client, invoices); err != nil {
		fmt.Printf("failed to create invoices table: %s\n", err)
		return
	}

	if _, err := rasql.Insert(ctx, client, invoices, invoiceRow{ID: 1, Amount: "19.99"}); err != nil {
		fmt.Printf("failed to insert invoice: %s\n", err)
		return
	}

	invoiceID, err := invoices.Column("id")
	if err != nil {
		fmt.Printf("failed to reference id column: %s\n", err)
		return
	}
	invoice, err := rasql.SelectFrom(client, invoices).WhereEqual(invoiceID, int64(1)).One(ctx)
	if err != nil {
		fmt.Printf("failed to query invoices: %s\n", err)
		return
	}

	fmt.Println(invoice.Amount)

	// Output:
	// 19.99
}
```
source: [examples/schema_decimal_column_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/schema_decimal_column_example_test.go)
<!-- END INCLUDE -->

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

A `rasql.Table[T]` is half of a table value rather than the whole of it. Wrap it in a type holding one `query.Column` field per column, so that `users.ID` is the column reference the builders take. That is the shape [`rasqlgen`](06-rasqlgen.md) emits, the shape every example on these pages uses, and the shape a hand-written table should have too. [Getting started](01-getting-started.md#the-table-used-throughout-the-documentation) shows the full wrapper for the `users` table, and [What the column fields catch](06-rasqlgen.md#what-the-column-fields-catch) shows what the fields are worth.

Two methods remain for code that only learns a column name while it runs. `users.Column(name)` looks a column up and returns a `query.Column` with an error, and `users.QueryTable()` returns the underlying `query.Table` that the lower-level `query` package works in terms of, which [Querying](03-querying.md) uses for joins and projections.

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

`inspect.New` takes the same kind of handle as `rasql.New` plus the dialect that describes the database being read. `Table` looks up one table by name. The result is an ordinary descriptor, so it can be validated, compared against a checked-in definition, or handed to the generator. A PostgreSQL `NUMERIC(p,s)` or MySQL `DECIMAL(p,s)` column normalizes to `schema.TypeDecimal` with `Precision` and `Scale` filled in from the catalog. Two decimal shapes still return an error rather than a descriptor: a PostgreSQL column declared as bare, unconstrained `numeric` has no precision the catalog can report, so `Table` refuses it rather than guess one; and any SQLite `DECIMAL`/`NUMERIC` column is refused outright, since such a column actually holds `REAL` values in SQLite (see [Logical column types](#logical-column-types) above) and `schema.TypeDecimal` would claim an exactness the stored data does not have. A SQLite column that rasql itself created as `TypeDecimal` was declared `TEXT`, and inspects back as `schema.TypeText`, not `schema.TypeDecimal`: SQLite's catalog does not record enough to recover the original logical type.

For PostgreSQL and SQLite, `Table` never returns a descriptor silently missing columns or a primary key. PostgreSQL's `information_schema` views are filtered by the inspecting role's privileges, while `pg_catalog` is not, so `inspect` reads the true column count and the primary key from `pg_catalog` rather than trusting `information_schema` alone. A role whose grants hide some or all of a table's columns gets `inspect.IncompleteMetadataError`, and a name that does not exist gets `inspect.TableNotFoundError`. A plain read-only role gets its primary key from `pg_catalog` too, so it sees a complete descriptor with no error. MySQL has the same `information_schema` filtering but no unfiltered catalog to cross-check against, so a restricted MySQL grant can make inspection silently under-report a table's columns or primary key, with no way for this package to detect it.

## Next

[Querying](03-querying.md) reads rows through these descriptors, or [Writing rows](04-writing.md) puts rows into them.
