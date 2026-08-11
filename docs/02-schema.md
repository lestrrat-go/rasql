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
	// This example defines two reusable table descriptors in Go code, built
	// with schema.MustTable. A column constructor such as schema.Integer and
	// a constraint constructor such as schema.PrimaryKey each return a
	// schema.TableOption, so they may appear in any order: PrimaryKey names
	// "id" below before Integer declares it, and the assembled descriptor is
	// the same either way. The same descriptor can later supply a reusable
	// query.Table or generate DDL.
	users := schema.MustTable("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.Text("nickname", schema.Nullable()),
		schema.Time("created_at", schema.Default("CURRENT_TIMESTAMP")),
		schema.Decimal("balance", 19, 4),
		schema.PrimaryKey("id"),
		schema.Unique("email"),
		schema.Index("users_email_idx", "email"),
		schema.Check("balance >= 0"),
	)

	// A foreign key's Named, References, and OnDelete options configure the
	// constraint itself. As additionally derives the belongs-to
	// schema.RelationshipDef that rasqlgen would otherwise name on its own
	// from the local column, letting the generated method read
	// orders.Buyer() rather than orders.Customer().
	orders := schema.MustTable("orders",
		schema.Integer("id"),
		schema.Integer("customer_id"),
		schema.PrimaryKey("id"),
		schema.ForeignKey("customer_id",
			schema.Named("orders_customer_fkey"),
			schema.References("customers", "id"),
			schema.OnDelete(schema.Cascade),
			schema.As("buyer")),
	)

	fmt.Printf("%s: %d columns, primary key %v\n", users.Name, len(users.Columns), users.PrimaryKey)
	fmt.Printf("%s: foreign key %s references %s, relationship %q\n",
		orders.Name, orders.ForeignKeys[0].Name, orders.ForeignKeys[0].ReferencedTable, orders.Relationships[0].Name)

	// Output:
	// users: 5 columns, primary key [id]
	// orders: foreign key orders_customer_fkey references customers, relationship "buyer"
}
```
source: [examples/schema_table_definition_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/schema_table_definition_example_test.go)
<!-- END INCLUDE -->

`schema.MustTable` panics on an invalid descriptor and suits a table declared
once at package initialization, exactly like `rasql.MustTableOf[T]`;
`schema.NewTable` returns the error instead, for a descriptor assembled at
runtime. Both collect the columns and constraints each `schema.TableOption`
declares and assemble them into a `schema.TableDef` afterward, which is what
makes the order harmless: `schema.PrimaryKey("id")` may appear before
`schema.Integer("id")` declares the column it names. Both then validate the
assembled descriptor exactly as `TableDef.Validate` would validate a struct
literal, so an unknown primary key column or a duplicate constraint name is
rejected the same way either form is built.

A column constructor such as `schema.Integer` or `schema.Text` takes zero or
more `schema.ColumnOption` values: `schema.Nullable()` marks the column
nullable, `schema.Default(expr)` states its default expression, and
`schema.Unsigned()` marks an integer column unsigned, rejected on every other
column type. `schema.Decimal` takes precision and scale as ordinary arguments
rather than options, since `TableDef.Validate` requires both anyway.

| Constructor | Declares |
| --- | --- |
| `schema.PrimaryKey` | The columns that uniquely identify each row. |
| `schema.Unique` / `schema.UniqueNamed` | An unnamed, or named, uniqueness requirement over columns. |
| `schema.Check` / `schema.CheckNamed` | An unnamed, or named, check constraint over an expression. |
| `schema.Index` / `schema.UniqueIndex` | A plain, or unique, secondary index over columns. |
| `schema.ForeignKey` / `schema.ForeignKeyOn` | A foreign key over one column, or over several. |
| `schema.InSchema` | The namespace qualifying the table. |

`schema.ForeignKey` takes the single local column and `schema.ForeignKeyOn`
takes a `[]string` of them for a composite key; both take the same list of
`schema.ForeignKeyOption` values: `schema.Named` states the constraint name,
`schema.References` states the target table and columns, `schema.ReferencesIn`
does the same for a target qualified by schema, `schema.OnDelete` and
`schema.OnUpdate` state the reference actions (`schema.Cascade`,
`schema.Restrict`, `schema.SetNull`, `schema.SetDefault`, and
`schema.NoAction`), and `schema.As` derives a belongs-to `RelationshipDef`
alongside it. Together these constructors cover every shape a struct literal
can express: a composite foreign key, a named unique constraint or check, and
a unique index all have an option-form constructor, with no need to fall back
to a struct literal for any of them.

## The struct literal

`schema.TableDef` is the descriptor itself; `schema.NewTable` and
`schema.MustTable` are one way to build one. Its fields are exactly what a
`schema.TableOption` assembles behind the scenes, and they are also what
`inspect` returns from a live database and what `migrate`'s diff compares
between two descriptors, so reading a descriptor back, whether from
`inspect.Table` or from a variable holding one, means reading this struct
rather than a list of options:

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
| `Relationships` | Optional named relationship metadata used by generated relationship APIs. |

A struct literal remains a fully supported way to build a `schema.TableDef`
directly, and every field takes a keyed composite literal such as
`schema.TableDef{Name: "orders", Columns: []schema.ColumnDef{...}, ...}`.
Call `Validate` before using a descriptor built this way. It reports a
`*schema.ValidationError` naming the part that is wrong, such as a primary
key that lists a column the table does not declare. Non-empty names given to
`UniqueConstraints`, `Checks`, and `ForeignKeys` must be unique across all
three, since a dialect renders them together into one `CREATE TABLE`
constraint list. `MustTable` and `NewTable` validate as well, so a separate
`Validate` call is only needed for a descriptor built at runtime that is not
immediately turned into a table.

## Relationships

`ForeignKeys` remain the source of database constraints. `rasqlgen` derives a `schema.RelationshipDef` with kind `schema.RelationshipBelongsTo` for each foreign key that has no matching entry in `Relationships`; the `schema.As` foreign-key option states one explicitly, in the option form, instead. Set `Relationships` explicitly when the generated method name should differ from the local column name, but keep its local columns and referenced schema, table, and columns matched to a declared foreign key. Relationship metadata does not change DDL.

The generated API covers one bounded slice: a non-null single-column foreign key that targets a non-null single-column primary key with the same generated Go type. When both tables are generated in the package, the child table exposes a belongs-to method and the parent table exposes the inverse has-many method. Each relation exposes `Join` and `Load`; `Load` fetches all related rows with one secondary `IN` query and groups them by key. Callers must split very large parent slices themselves when they approach the database parameter limit.

Composite keys, nullable foreign keys, nullable or non-primary target columns, many-to-many links, polymorphic links, nested preloading, and relationships whose target table is not generated in the package remain unsupported. The foreign key and its ordinary SQL join remain available for each of those cases.

## Qualify a table with a schema

`Schema` is optional and names the namespace holding the table: a PostgreSQL schema, a MySQL database, or a SQLite attached-database name. rasql takes no position on what a namespace means to a server: it validates `Schema` as a simple identifier exactly like `Name`, quotes it as a separate identifier in the SQL that reads the field, and never creates, drops, or connects to a namespace itself. An application that needs `audit.events` to exist creates it with a reviewed native migration, the same way every other piece of DDL this library does not synthesize gets created. An empty `Schema` leaves the table unqualified, which resolves through the connection's own default and is what every descriptor written before this field existed still does.

Qualification reaches DML, column references, and DDL. A `SELECT`, `INSERT`, `UPDATE`, or `DELETE` built from a qualified descriptor renders `"audit"."events"` as its target, a column reached through the unaliased table renders `"audit"."events"."id"`, and `render.CreateTable`, `render.CreateIndexes`, and `rasql.Create` render `CREATE TABLE "audit"."events"` and its indexes into the named namespace on every dialect that can express it. rasql never creates, drops, or connects to the namespace itself: an application that needs `audit` to exist creates it with a reviewed native migration, the same way every other piece of DDL this library does not synthesize gets created, and `rasql.Create` then fails with the server's own error if that namespace does not exist. SQLite inspection preserves the database name in `Schema`, including when a lookup is scoped with `TableIn`, and [`rasqlgen`](06-rasqlgen.md) emits that non-empty `Schema` value in generated descriptors. PostgreSQL and MySQL inspection leave `Schema` empty, so `rasqlgen` emits no `Schema` field for those dialects. Qualified PostgreSQL and MySQL inspection and generation are not supported yet, so a qualified table on those dialects is re-read through a hand-written descriptor.

A foreign key that references a table in another schema names it with `ForeignKeyDef.ReferencedSchema`, validated the same way as `Table.Schema` and left empty for the server to resolve, exactly like an empty `Table.Schema`. PostgreSQL and MySQL render a stated `ReferencedSchema` as a second qualified identifier in the `REFERENCES` clause. SQLite cannot: it rejects a schema-qualified `REFERENCES` outright, even when the reference names the referencing table's own schema, so rasql drops a same-schema qualifier there rather than refuse a reference that means the same thing either way, and refuses to render a genuinely cross-schema reference instead of silently pointing it at the wrong table. An unqualified table's foreign keys are unaffected either way: qualifying `Table.Schema` alone, without also stating `ForeignKeyDef.ReferencedSchema`, would let PostgreSQL resolve an unqualified `REFERENCES` through the connection's `search_path` rather than the table's own schema, which is why the two fields ship together.

`schema.TableDef` and `query.Table` each answer two questions about qualification. `Qualified` reports whether a schema is named at all, and `QualifiedName` returns `schema.name` for display, falling back to `name` for an unqualified table. Neither is a SQL identifier: a renderer quotes `Schema` and `Name` as two identifiers, and `dialect.QuoteIdentifier` rejects the dotted string `QualifiedName` returns. On `query.Table` the two describe the table rather than the reference: `Qualified` stays true once the table is aliased, while `QualifiedName` returns the alias, because that is what an error message about an aliased table has to name. `query.Table.QualifierSchema` reports what actually qualifies a rendered column, which is nothing at all once an alias replaces the table's whole name.

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
	// This example creates and queries a table through a schema-qualified
	// descriptor. Schema names a PostgreSQL schema, a MySQL database, or, as
	// here, a SQLite attached-database name. rasql never creates the
	// namespace itself, so the ATTACH DATABASE below stands in for a
	// reviewed native migration, which is the only way rasql creates a
	// namespace in production; rasql.Create then renders CREATE TABLE
	// "audit"."events" into the namespace that migration already created.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	if _, err := database.ExecContext(ctx, `ATTACH DATABASE ':memory:' AS audit`); err != nil {
		fmt.Printf("failed to attach audit database: %s\n", err)
		return
	}

	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}

	// InSchema qualifies the table without changing how any other option works.
	events := rasql.MustTableOf[eventRow](schema.MustTable("events",
		schema.InSchema("audit"),
		schema.Integer("id"),
		schema.Text("action"),
		schema.PrimaryKey("id"),
	))

	// SQL: CREATE TABLE audit.events (id INTEGER NOT NULL, action TEXT NOT NULL, PRIMARY KEY (id))
	if err := rasql.Create(ctx, client, events); err != nil {
		fmt.Printf("failed to create events table: %s\n", err)
		return
	}

	// SQL: INSERT INTO audit.events (id, action) VALUES (?, ?) (arguments: 1, "created")
	if _, err := rasql.Insert(ctx, client, events, eventRow{ID: 1, Action: "created"}); err != nil {
		fmt.Printf("failed to insert event: %s\n", err)
		return
	}

	eventID, err := events.Column("id")
	if err != nil {
		fmt.Printf("failed to reference id column: %s\n", err)
		return
	}
	// SQL: SELECT audit.events.id, audit.events.action FROM audit.events WHERE audit.events.id = ? (argument: 1)
	event, err := rasql.SelectFrom(events).WhereEqual(eventID, int64(1)).One(ctx, client)
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
| `schema.BooleanType` | `bool` |
| `schema.IntegerType` | `int64` |
| `schema.FloatType` | `float64` |
| `schema.TextType` | `string` |
| `schema.BytesType` | `[]byte` |
| `schema.TimeType` | `time.Time` |
| `schema.JSONType` | `[]byte` or a type that marshals itself |
| `schema.UUIDType` | `string` or a UUID type |
| `schema.DecimalType` | `string` |

A column also carries `Nullable`, `Default`, and its concrete `Type`. Type-specific options live on that type: `IntegerType.Unsigned` describes unsigned integers, while `DecimalType` carries `Precision` and `Scale`. Identifiers must be simple: `schema.ValidateIdentifier` accepts a leading letter or underscore followed by letters, digits, or underscores, and everything else is rejected rather than quoted around.

`schema.DecimalType` is an exact decimal, for money, quantities, and any other value a binary floating-point `FloatType` would round. A decimal type must set `Precision` (the total number of significant digits, at least 1) and `Scale` (the number of those digits right of the decimal point, no more than `Precision`); `TableDef.Validate` rejects a decimal type that omits either. `Scale` is a `schema.DecimalScale` rather than a plain `int`, and is stated with `schema.NewDecimalScale`, because a `DECIMAL(19,0)` column is legitimate and its zero scale has to be distinguishable from a descriptor that named no scale at all; the zero value of `schema.DecimalScale` means "no scale stated" and `DecimalScale.Value` returns the stated scale together with whether one was stated. Each dialect renders `Precision`/`Scale` into its own DDL: PostgreSQL and MySQL render `NUMERIC(p,s)` and `DECIMAL(p,s)`, each exact and each enforcing its own maximum precision and scale. On both, a decimal column decodes to its declared scale in string form, zero-padded on the right: a `NUMERIC(19,4)` column yields `"19.9900"` for the value `19.99`, not `"19.99"`, so a caller comparing decimal strings has to compare on the declared scale. That declared scale governs the column itself; a projected expression over it need not keep it, and [Scalar functions](03-querying.md#scalar-functions) states where MySQL widens one. SQLite has no exact decimal storage class, so it renders `TEXT` instead: the column round-trips its digits exactly and applies no such padding, decoding to a Go `string` on every dialect, but a SQLite decimal column compares and orders lexicographically rather than numerically, since it is stored as text rather than a number. A caller that wants a real decimal type in Go, rather than a `string`, can write its own row struct with a field implementing `sql.Scanner` and `driver.Valuer`; `row.Assign` checks for that interface before every built-in conversion, so the raw driver value reaches it unchanged.

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

// invoiceRow maps the one schema.DecimalType column this example declares.
// The column decodes into a Go string on every dialect. This example runs on
// SQLite, which stores such a column as TEXT and hands back the exact digits
// inserted; PostgreSQL and MySQL instead return the value in the column's
// declared scale, so the same "19.99" reads back as "19.9900" there.
type invoiceRow struct {
	ID     int64  `rasql:"id"`
	Amount string `rasql:"amount"`
}

func Example_schema_decimal_column() {
	// This example declares a schema.DecimalType column, creates its table in
	// SQLite, and shows that the inserted string round-trips unchanged there.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}

	// schema.Decimal takes precision and scale positionally, rather than as
	// options, because TableDef.Validate rejects a decimal column that lacks
	// either: stating both here makes an incomplete decimal column impossible
	// to construct in the first place instead of merely rejected once
	// assembled.
	invoices := rasql.MustTableOf[invoiceRow](schema.MustTable("invoices",
		schema.Integer("id"),
		schema.Decimal("amount", 19, 4),
		schema.PrimaryKey("id"),
	))
	// SQLite has no exact decimal storage class, so the dialect declares this
	// column TEXT rather than NUMERIC(19,4), which would round through REAL.
	// SQL: CREATE TABLE invoices (id INTEGER NOT NULL, amount TEXT NOT NULL, PRIMARY KEY (id))
	if err := rasql.Create(ctx, client, invoices); err != nil {
		fmt.Printf("failed to create invoices table: %s\n", err)
		return
	}

	// SQL: INSERT INTO invoices (id, amount) VALUES (?, ?) (arguments: 1, "19.99")
	if _, err := rasql.Insert(ctx, client, invoices, invoiceRow{ID: 1, Amount: "19.99"}); err != nil {
		fmt.Printf("failed to insert invoice: %s\n", err)
		return
	}

	invoiceID, err := invoices.Column("id")
	if err != nil {
		fmt.Printf("failed to reference id column: %s\n", err)
		return
	}
	// SQL: SELECT invoices.id, invoices.amount FROM invoices WHERE invoices.id = ? (argument: 1)
	invoice, err := rasql.SelectFrom(invoices).WhereEqual(invoiceID, int64(1)).One(ctx, client)
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

A `schema.IntegerType` column is signed unless `IntegerType.Unsigned` is true. An unsigned column stores no negative values and reaches 18446744073709551615 instead of 9223372036854775807. Other concrete column types cannot carry this option. `TableDef.Validate` still checks the type-specific values, while dialects reject unsigned integers when they have no unsigned integer syntax.

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
	events := schema.MustTable("events",
		// An unsigned column reaches 18446744073709551615, where a signed one
		// stops at 9223372036854775807. rasqlgen generates a uint64 field for
		// it rather than an int64 one.
		schema.Integer("id", schema.Unsigned()),
		schema.Integer("sequence"),
		schema.PrimaryKey("id"),
	)

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

A bare `schema.TableDef` describes the database. Pairing it with a Go type produces a `rasql.Table[T]`, which is what the typed API takes:

```go
type UserRow struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

users := rasql.MustTableOf[UserRow](definition)
```

Each field's `rasql` tag names the column it holds. `rasql.MustTableOf` panics on an invalid descriptor and suits generated or otherwise constant tables; `rasql.TableOf` returns the error instead, for descriptors assembled at runtime.

A `rasql.Table[T]` is half of a table value rather than the whole of it. Wrap it in a type holding one `query.Column` field per column, so that `users.ID` is the column reference the builders take. That is the shape [`rasqlgen`](06-rasqlgen.md) emits, the shape every example on these pages uses, and the shape a hand-written table should have too. [Getting started](01-getting-started.md#the-table-used-throughout-the-documentation) shows the full wrapper for the `users` table, and [What the column fields catch](06-rasqlgen.md#what-the-column-fields-catch) shows what the fields are worth.

Two methods remain for code that only learns a column name while it runs. `users.Column(name)` looks a column up and returns a `query.Column` with an error, and `users.QueryTable()` returns the underlying `query.Table` that the lower-level `query` package works in terms of, which [Querying](03-querying.md) uses for joins and projections.

## Read a table out of a database

`inspect` turns live database metadata back into a `schema.TableDef`, normalizing native column types into logical ones. `Inspector.Table` looks up an unscoped table name. On SQLite, it searches `main`, `temp`, and attached databases; if the name exists in more than one of them, it returns the typed `*inspect.AmbiguousTableError` (also detectable with `inspect.ErrAmbiguousTable`) instead of choosing one. Use `Inspector.TableIn(ctx, databaseName, tableName)` to select `main`, `temp`, or an attached database. The returned `schema.TableDef.Schema` preserves that SQLite database name, so rendering or executing the descriptor continues to address the inspected scope. `inspect.New` accepts a SQLite `*sql.DB` for ordinary `main` tables. A retained `*sql.Conn` or `*sql.Tx` is required for `temp` or an attached database, and the same handle must execute descriptors that refer to those scopes because they belong to one connection rather than the `*sql.DB` pool. `TableIn` is supported only for SQLite. The inspector falls back to each database's `sqlite_master` catalog when `PRAGMA table_list` is unavailable on older SQLite engines.

<!-- INCLUDE(examples/inspect_sqlite_table_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_inspect_sqlite_table() {
	// This example reads SQLite tables from main, temp, and an attached database.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	connection, err := database.Conn(ctx)
	if err != nil {
		fmt.Printf("failed to retain SQLite connection: %s\n", err)
		return
	}
	defer func() { _ = connection.Close() }()
	// Pretend these tables already exist in an application-owned SQLite database.
	if _, err := connection.ExecContext(ctx, "ATTACH DATABASE ':memory:' AS aux"); err != nil {
		fmt.Printf("failed to attach aux database: %s\n", err)
		return
	}
	for _, statement := range []string{
		"CREATE TABLE main.users (id INTEGER PRIMARY KEY, main_value TEXT)",
		"CREATE TABLE aux.users (id INTEGER PRIMARY KEY, aux_value TEXT)",
		"CREATE TEMP TABLE users (id INTEGER PRIMARY KEY, temp_value TEXT)",
	} {
		if _, err := connection.ExecContext(ctx, statement); err != nil {
			fmt.Printf("failed to create users table: %s\n", err)
			return
		}
	}

	// An unscoped lookup does not guess when several databases contain users.
	// The typed error exposes the conflicting database names to the caller.
	// SQLite inspection stays on the retained connection because temp and
	// attached databases belong to that connection.
	inspector, err := inspect.New(connection, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create SQLite inspector: %s\n", err)
		return
	}
	_, err = inspector.Table(ctx, "users")
	var ambiguous *inspect.AmbiguousTableError
	if !errors.As(err, &ambiguous) {
		fmt.Printf("expected ambiguous users error, got %v\n", err)
		return
	}
	fmt.Printf("ambiguous %s: %d databases\n", ambiguous.Table, len(ambiguous.Databases))

	for _, databaseName := range []string{"main", "temp", "aux"} {
		table, err := inspector.TableIn(ctx, databaseName, "users")
		if err != nil {
			fmt.Printf("failed to inspect %s.users: %s\n", databaseName, err)
			return
		}
		fmt.Printf("%s.%s: %s\n", table.Schema, table.Name, table.Columns[1].Name)
	}

	// Output:
	// ambiguous users: 3 databases
	// main.users: main_value
	// temp.users: temp_value
	// aux.users: aux_value
}
```
source: [examples/inspect_sqlite_table_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/inspect_sqlite_table_example_test.go)
<!-- END INCLUDE -->

`inspect.New` takes the same kind of handle as `rasql.New` plus the dialect that describes the database being read. The result is an ordinary descriptor, so it can be validated, compared against a checked-in definition, or handed to the generator. A PostgreSQL `NUMERIC(p,s)` or MySQL `DECIMAL(p,s)` column normalizes to `schema.DecimalType` with `Precision` and `Scale` filled in from the catalog. Catalog metadata comes from whichever server the application points at, so a decimal is recognized only from a type declaration matched in full, never from a substring of one: on MySQL, `COLUMN_TYPE` must read exactly `DECIMAL` or `NUMERIC`, optionally followed by `(precision)` or `(precision, scale)`, and catalog text such as `FOODECIMALBAR` is an unsupported type rather than a decimal. Four decimal shapes return an error rather than a descriptor: a PostgreSQL column declared as bare, unconstrained `numeric` has no precision the catalog can report, so `Table` refuses it rather than guess one; a decimal column whose catalog row reports no scale is refused for the same reason, since recording the missing scale as 0 would drop the column's fractional digits; a MySQL `DECIMAL`/`NUMERIC` declaration carrying `UNSIGNED`, `ZEROFILL` or any other modifier is refused, because `DecimalType` cannot record the modifier and re-rendering the column without it would change the values the column permits; and any SQLite `DECIMAL`/`NUMERIC` column is refused outright, since such a column actually holds `REAL` values in SQLite (see [Logical column types](#logical-column-types) above) and `schema.DecimalType` would claim an exactness the stored data does not have. A SQLite column that rasql itself created as `DecimalType` was declared `TEXT`, and inspects back as `schema.TextType`, not `schema.DecimalType`: SQLite's catalog does not record enough to recover the original logical type.

Integer declarations are matched the same way, and for the same reason. On MySQL, `COLUMN_TYPE` must read exactly `TINYINT`, `SMALLINT`, `MEDIUMINT`, `INT`, `INTEGER` or `BIGINT`, optionally followed by a display width and then by `UNSIGNED`; a declaration carrying `UNSIGNED` sets `IntegerType.Unsigned`, and one carrying `ZEROFILL` or any other modifier is refused, since the concrete type cannot record it. Matching the whole declaration is what makes the `UNSIGNED` visible at all: a substring test on `INT` cannot see what follows the type, which is how a `bigint(20) unsigned` column used to inspect as a plain signed integer and re-render as `BIGINT`, losing every value above 9223372036854775807. It also accepted MySQL's `POINT`, which is not an integer at all and is now an unsupported type. PostgreSQL has no unsigned integer type and SQLite stores a signed 64-bit value whatever a column is declared, so neither ever reports an unsigned column; a SQLite column declared `UNSIGNED BIG INT` inspects as the signed integer column it really is.

For PostgreSQL and SQLite, `Table` never returns a descriptor silently missing columns or a primary key. PostgreSQL's `information_schema` views are filtered by the inspecting role's privileges, while `pg_catalog` is not, so `inspect` reads the true column count and the primary key from `pg_catalog` rather than trusting `information_schema` alone. A role whose grants hide some or all of a table's columns gets `inspect.IncompleteMetadataError`, and a name that does not exist gets `inspect.TableNotFoundError`. A plain read-only role gets its primary key from `pg_catalog` too, so it sees a complete descriptor with no error. MySQL filters `information_schema.columns` by column privileges, so `inspect` cross-checks the visible column count against the full `SHOW CREATE TABLE` definition and returns `inspect.ErrIncompleteMetadata` when a restricted grant hides columns. SQLite has no privilege filtering.

## Next

[Querying](03-querying.md) reads rows through these descriptors, or [Writing rows](04-writing.md) puts rows into them.
