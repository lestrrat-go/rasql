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
| `Schema` | The optional namespace holding the table. |
| `Name` | The table identifier. |
| `Columns` | Each column, in the order it is declared. |
| `PrimaryKey` | Column names from `Columns` that identify a row. |
| `UniqueConstraints` | Named or unnamed uniqueness requirements. |
| `Checks` | Check constraints. |
| `Indexes` | Secondary indexes. |
| `ForeignKeys` | References to other tables, with their update and delete actions. |

Call `Validate` before using a descriptor. It reports a `*schema.ValidationError` naming the part that is wrong, such as a primary key that lists a column the table does not declare. Non-empty names given to `UniqueConstraints`, `Checks`, and `ForeignKeys` must be unique across all three, since a dialect renders them together into one `CREATE TABLE` constraint list. `MustTable` and `NewTable` validate as well, so a separate `Validate` call is only needed for a descriptor built at runtime that is not immediately turned into a table.

## Qualify a table with a schema

`Schema` is optional and names the namespace holding the table: a PostgreSQL schema, a MySQL database, or a SQLite attached-database name. rasql takes no position on what a namespace means to a server: it validates `Schema` as a simple identifier exactly like `Name`, quotes it as a separate identifier wherever it renders a table or a column reference, and never creates, drops, or connects to one itself. An application that needs `audit.events` to exist creates it with a reviewed native migration, the same way every other piece of DDL this library does not synthesize gets created. An empty `Schema` leaves the table unqualified, which resolves through the connection's own default and is what every descriptor written before this field existed still does.

<!-- INCLUDE(examples/schema_qualified_table_example_test.go) -->
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

// eventRow maps the qualified "audit.events" table this example queries.
type eventRow struct {
	ID     int64  `rasql:"id"`
	Action string `rasql:"action"`
}

func Example_schema_qualified_table() {
	// This example queries a table through a schema-qualified descriptor.
	// Schema names a PostgreSQL schema, a MySQL database, or, as here, a
	// SQLite attached-database name. rasql never creates the namespace
	// itself, so the CREATE TABLE below stands in for a reviewed native
	// migration, which is the only way rasql creates a schema in production.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer database.Close()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	if _, err := database.ExecContext(ctx, `ATTACH DATABASE ':memory:' AS audit`); err != nil {
		fmt.Printf("failed to attach audit database: %s\n", err)
		return
	}
	if _, err := database.ExecContext(ctx, "CREATE TABLE audit.events (id INTEGER PRIMARY KEY, action TEXT NOT NULL)"); err != nil {
		fmt.Printf("failed to create events table: %s\n", err)
		return
	}

	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}

	// Schema qualifies the table without changing how any other field works.
	events := rasql.MustTable[eventRow](schema.Table{
		Schema: "audit",
		Name:   "events",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "action", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})

	if _, err := rasql.Insert(ctx, client, events, eventRow{ID: 1, Action: "created"}); err != nil {
		fmt.Printf("failed to insert event: %s\n", err)
		return
	}

	eventID, err := events.Column("id")
	if err != nil {
		fmt.Printf("failed to reference id column: %s\n", err)
		return
	}
	event, err := rasql.SelectFrom(client, events).WhereEqual(eventID, int64(1)).One(ctx)
	if err != nil {
		fmt.Printf("failed to query events: %s\n", err)
		return
	}

	// QualifiedName is for display only, never a SQL identifier: the renderer
	// quotes Schema and Name as two separate identifiers.
	fmt.Printf("%s: %s\n", events.QueryTable().QualifiedName(), event.Action)

	// Output:
	// audit.events: created
}
```
source: [examples/schema_qualified_table_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/schema_qualified_table_example_test.go)
<!-- END INCLUDE -->

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

A column is also `Nullable` or not, may be `Unsigned` (see [Unsigned integer columns](#unsigned-integer-columns) below, where the typical Go value is `uint64` rather than `int64`), and may carry a `Default` written as SQL text. Identifiers must be simple: `schema.ValidateIdentifier` accepts a leading letter or underscore followed by letters, digits, or underscores, and everything else is rejected rather than quoted around.

`schema.TypeDecimal` is an exact decimal, for money, quantities, and any other value a binary floating-point `TypeFloat` would round. A decimal column must set `Precision` (the total number of significant digits, at least 1) and `Scale` (the number of those digits right of the decimal point, no more than `Precision`); `Table.Validate` rejects a decimal column that omits either and rejects `Precision` or `Scale` on any other logical type. `Scale` is a `schema.DecimalScale` rather than a plain `int`, and is stated with `schema.NewDecimalScale`, because a `DECIMAL(19,0)` column is legitimate and its zero scale has to be distinguishable from a descriptor that named no scale at all; the zero value of `schema.DecimalScale` means "no scale stated" and `DecimalScale.Value` returns the stated scale together with whether one was stated. Each dialect renders `Precision`/`Scale` into its own DDL: PostgreSQL and MySQL render `NUMERIC(p,s)` and `DECIMAL(p,s)`, each exact and each enforcing its own maximum precision and scale. On both, a decimal column decodes to its declared scale in string form, zero-padded on the right: a `NUMERIC(19,4)` column yields `"19.9900"` for the value `19.99`, not `"19.99"`, so a caller comparing decimal strings has to compare on the declared scale. SQLite has no exact decimal storage class, so it renders `TEXT` instead: the column round-trips its digits exactly and applies no such padding, decoding to a Go `string` on every dialect, but a SQLite decimal column compares and orders lexicographically rather than numerically, since it is stored as text rather than a number. A caller that wants a real decimal type in Go, rather than a `string`, can write its own row struct with a field implementing `sql.Scanner` and `driver.Valuer`; `row.Assign` checks for that interface before every built-in conversion, so the raw driver value reaches it unchanged.

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
// The column decodes into a Go string on every dialect. This example runs on
// SQLite, which stores such a column as TEXT and hands back the exact digits
// inserted; PostgreSQL and MySQL instead return the value in the column's
// declared scale, so the same "19.99" reads back as "19.9900" there.
type invoiceRow struct {
	ID     int64  `rasql:"id"`
	Amount string `rasql:"amount"`
}

func Example_schema_decimal_column() {
	// This example declares a schema.TypeDecimal column, creates its table in
	// SQLite, and shows that the inserted string round-trips unchanged there.
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
	// rejects a decimal column that omits either. Scale is stated through
	// schema.NewDecimalScale so that a scale of 0 is distinguishable from a
	// column that named no scale at all.
	invoices := rasql.MustTable[invoiceRow](schema.Table{
		Name: "invoices",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "amount", Type: schema.TypeDecimal, Precision: 19, Scale: schema.NewDecimalScale(4)},
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

## Unsigned integer columns

A `schema.TypeInteger` column is signed unless it sets `Unsigned`, which states that the column stores no negative values and so reaches 18446744073709551615 instead of 9223372036854775807. `Unsigned` is a plain `bool` rather than a value type like `Scale`, because signedness has exactly two states and the default is the signed one every descriptor written before this field already meant. `Table.Validate` rejects `Unsigned` on any other logical type, since no other logical type has a signedness for a dialect to render.

Engines differ here, and rasql says so instead of papering over it. MySQL has unsigned integer types and renders such a column `BIGINT UNSIGNED`. PostgreSQL has none, and SQLite stores a signed 64-bit value whatever a column is declared, so both report an error naming the column rather than render a signed `BIGINT` that would reject the values the descriptor permits. A schema that has to run on all three declares the column signed, and narrows the range it claims to what every engine can hold.

Only `BIGINT UNSIGNED` actually gains range from this. Every narrower unsigned type — `TINYINT UNSIGNED` through `INT UNSIGNED` — fits inside a signed `BIGINT` already, so a column of one of those loses no representable value either way; what it gains is that the descriptor now says what the column is, and re-rendering it keeps the `UNSIGNED` the database had.

[`rasqlgen`](06-rasqlgen.md) generates a `uint64` field for an unsigned column instead of an `int64` one, because `int64` cannot hold the top half of the range. `row.Assign` fills either field from an integer driver value of either signedness and reports an error, rather than wrapping, for a value the field cannot hold: which signedness a driver delivers is the driver's choice rather than the column's.

<!-- INCLUDE(examples/schema_unsigned_column_example_test.go) -->
```go
package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

func Example_schema_unsigned_column() {
	// This example declares an unsigned integer column and renders its DDL for
	// each dialect. MySQL is the only supported engine with an unsigned
	// integer type, so it is the only one that renders the table.
	events := schema.Table{
		Name: "events",
		Columns: []schema.Column{
			// An unsigned column reaches 18446744073709551615, where a signed
			// one stops at 9223372036854775807. rasqlgen generates a uint64
			// field for it rather than an int64 one.
			{Name: "id", Type: schema.TypeInteger, Unsigned: true},
			{Name: "sequence", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	}
	if err := events.Validate(); err != nil {
		fmt.Printf("failed to define table: %s\n", err)
		return
	}

	mysql, err := render.CreateTable(dialect.MySQL(), events)
	if err != nil {
		fmt.Printf("failed to render MySQL DDL: %s\n", err)
		return
	}
	fmt.Println(mysql.SQL())

	// PostgreSQL has no unsigned integer type, and SQLite stores a signed
	// 64-bit value whatever a column is declared. Both report an error naming
	// the column rather than render a signed BIGINT, which would reject the
	// values above 9223372036854775807 that the descriptor permits.
	for _, d := range []dialect.Dialect{dialect.PostgreSQL(), dialect.SQLite()} {
		if _, err := render.CreateTable(d, events); err != nil {
			fmt.Printf("%s refuses the column: %s\n", d.Name(), err)
		}
	}

	// Output:
	// CREATE TABLE `events` (`id` BIGINT UNSIGNED NOT NULL, `sequence` BIGINT NOT NULL, PRIMARY KEY (`id`))
	// postgresql refuses the column: render postgresql: column "id": dialect postgresql: unsigned integer column "id" cannot be represented: this dialect has no unsigned integer type, and rendering the column signed would narrow the values it permits
	// sqlite refuses the column: render sqlite: column "id": dialect sqlite: unsigned integer column "id" cannot be represented: this dialect has no unsigned integer type, and rendering the column signed would narrow the values it permits
}
```
source: [examples/schema_unsigned_column_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/schema_unsigned_column_example_test.go)
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

`inspect.New` takes the same kind of handle as `rasql.New` plus the dialect that describes the database being read. `Table` looks up one table by name. The result is an ordinary descriptor, so it can be validated, compared against a checked-in definition, or handed to the generator. A PostgreSQL `NUMERIC(p,s)` or MySQL `DECIMAL(p,s)` column normalizes to `schema.TypeDecimal` with `Precision` and `Scale` filled in from the catalog. Catalog metadata comes from whichever server the application points at, so a decimal is recognized only from a type declaration matched in full, never from a substring of one: on MySQL, `COLUMN_TYPE` must read exactly `DECIMAL` or `NUMERIC`, optionally followed by `(precision)` or `(precision, scale)`, and catalog text such as `FOODECIMALBAR` is an unsupported type rather than a decimal. Four decimal shapes return an error rather than a descriptor: a PostgreSQL column declared as bare, unconstrained `numeric` has no precision the catalog can report, so `Table` refuses it rather than guess one; a decimal column whose catalog row reports no scale is refused for the same reason, since recording the missing scale as 0 would drop the column's fractional digits; a MySQL `DECIMAL`/`NUMERIC` declaration carrying `UNSIGNED`, `ZEROFILL` or any other modifier is refused, because `schema.Column` cannot record the modifier and re-rendering the column without it would change the values the column permits; and any SQLite `DECIMAL`/`NUMERIC` column is refused outright, since such a column actually holds `REAL` values in SQLite (see [Logical column types](#logical-column-types) above) and `schema.TypeDecimal` would claim an exactness the stored data does not have. A SQLite column that rasql itself created as `TypeDecimal` was declared `TEXT`, and inspects back as `schema.TypeText`, not `schema.TypeDecimal`: SQLite's catalog does not record enough to recover the original logical type.

Integer declarations are matched the same way, and for the same reason. On MySQL, `COLUMN_TYPE` must read exactly `TINYINT`, `SMALLINT`, `MEDIUMINT`, `INT`, `INTEGER` or `BIGINT`, optionally followed by a display width and then by `UNSIGNED`; a declaration carrying `UNSIGNED` sets `Column.Unsigned`, and one carrying `ZEROFILL` or any other modifier is refused, since `schema.Column` cannot record it. Matching the whole declaration is what makes the `UNSIGNED` visible at all: a substring test on `INT` cannot see what follows the type, which is how a `bigint(20) unsigned` column used to inspect as a plain signed integer and re-render as `BIGINT`, losing every value above 9223372036854775807. It also accepted MySQL's `POINT`, which is not an integer at all and is now an unsupported type. PostgreSQL has no unsigned integer type and SQLite stores a signed 64-bit value whatever a column is declared, so neither ever reports an unsigned column; a SQLite column declared `UNSIGNED BIG INT` inspects as the signed integer column it really is.

For PostgreSQL and SQLite, `Table` never returns a descriptor silently missing columns or a primary key. PostgreSQL's `information_schema` views are filtered by the inspecting role's privileges, while `pg_catalog` is not, so `inspect` reads the true column count and the primary key from `pg_catalog` rather than trusting `information_schema` alone. A role whose grants hide some or all of a table's columns gets `inspect.IncompleteMetadataError`, and a name that does not exist gets `inspect.TableNotFoundError`. A plain read-only role gets its primary key from `pg_catalog` too, so it sees a complete descriptor with no error. MySQL has the same `information_schema` filtering but no unfiltered catalog to cross-check against, so a restricted MySQL grant can make inspection silently under-report a table's columns or primary key, with no way for this package to detect it.

## Next

[Querying](03-querying.md) reads rows through these descriptors, or [Writing rows](04-writing.md) puts rows into them.
