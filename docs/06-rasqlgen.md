# `rasqlgen`

`rasqlgen` writes Go source: table descriptors from a database or a schema snapshot, and query functions from [static templates](05-templates.md). Its output is deterministic, so regenerating unchanged input produces identical files and an accidental edit shows up in review.

Run it from the module without installing a binary:

```sh
go get github.com/lestrrat-go/rasql/cmd/rasqlgen@latest
go run github.com/lestrrat-go/rasql/cmd/rasqlgen <command> [flags]
```

Both `schema` and `query` take flags only, and every flag must precede any other argument: flag parsing stops at the first non-flag argument, so any other argument, or a flag placed after one, is rejected as an unexpected argument.

## `rasqlgen schema`

```sh
mkdir -p internal/store
go run github.com/lestrrat-go/rasql/cmd/rasqlgen schema \
  -dsn "$DATABASE_URL" \
  -table users \
  -table orders \
  -package store \
  -output internal/store
```

| Flag | Meaning |
| --- | --- |
| `-dsn` | Database connection string to inspect. |
| `-input` | Path to a JSON array of table descriptors, instead of `-dsn`. |
| `-table` | Table to generate; repeat it for each table. Required with `-dsn`; filters a JSON snapshot from `-input`. Passing the same table name twice is an error. |
| `-dialect` | Dialect and driver for `-dsn`, defaulting to `postgresql`. |
| `-timeout` | Deadline for `-dsn` metadata inspection, defaulting to 30s. The deadline does not apply to `-input`, but every `schema` invocation rejects a zero or negative value. |
| `-package` | Package name for the generated files. Required. |
| `-output` | Existing directory for generated files. Required. |

Supply either `-dsn` or `-input`, never both. Direct inspection supports PostgreSQL, MySQL, and SQLite; the command bundles their `pgx`, `mysql`, and `sqlite` drivers, so nothing needs importing to use `-dsn`. PostgreSQL inspection preserves supported columns, primary keys, named unique constraints, checks, ordinary B-tree indexes, and foreign keys, including exact decimal columns (`NUMERIC`/`DECIMAL`), which generate a Go `string` field and whose generated descriptor restates the column's `Precision` and `Scale`. MySQL inspection preserves supported columns, primary keys, and ordinary indexes; SQLite inspection currently preserves supported columns and primary keys. <!-- MySQL includes ordinary indexes here; SQLite does not, and no later clause contradicts that distinction. --> A generated `schema.Decimal` call restates precision and scale positionally, and states scale even when it is `0`, because the zero value of `schema.DecimalScale` means no scale was stated and `TableDef.Validate` rejects a decimal column that states none. It reports an error when an inspected type, index, or constraint has metadata that the schema descriptor cannot reproduce, and when a PostgreSQL column is a bare, unconstrained `NUMERIC`, since PostgreSQL reports no precision for it to record.

`schema` writes one file per table, named `<table>_gen.go` in lowercase. The example writes `users_gen.go` and `orders_gen.go` in `internal/store`.

When selected tables contain supported foreign keys, `rasqlgen` also emits typed relationship descriptors in those files. It uses the complete selected-table set to connect each generated file to its target, so a relationship method is emitted only when the target table is selected too. See [Relationships](02-schema.md#relationships) for the supported slice and its eager-loading behavior.

`-dsn` reads every requested table in one transaction and commits it after the last read, so a migration that commits partway through cannot split the generated descriptor across two schema versions. It requests repeatable-read and read-only modes where the driver supports them. `-timeout` bounds that whole transaction, from opening it through the last metadata query; a server that accepts the connection but never answers cancels the command instead of hanging it indefinitely.

Live MySQL inspection requires metadata privileges that expose every column in the selected table. MySQL filters `information_schema.columns` by column privileges, so a partial grant can otherwise produce an incomplete baseline; the inspector cross-checks the count against `SHOW CREATE TABLE` and returns `inspect.ErrIncompleteMetadata` instead. Grant table- or database-level `SELECT` (or equivalent full column visibility) before using MySQL live inspection or `diff-live`.

`-input` reads the same descriptors as JSON, which is how a checked-in snapshot works: inspect the database once, marshal the resulting `schema.TableDef` values, commit the file, and generate from it afterwards. Generation then needs no database, so a build or CI run stays offline. Without `-table`, the command generates every table in the snapshot. With one or more `-table` flags, it generates only those named tables and fails if the snapshot does not contain any requested name. Repeating the same `-table` value, whether with `-input` or `-dsn`, is rejected as a flag-parsing error rather than silently collapsed to one table. `-input` is capped at 64 MiB; a larger file is rejected before it is parsed.

### What it generates

For a `users` table of `id` and `email` the file declares a row type with its scan methods and its column-value method, a table type with one field per column, the descriptor, and a table accessor:

<!-- INCLUDE(examples/store/users_gen.go) -->
```go
// Code generated by rasqlgen; DO NOT EDIT.

package store

import (
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/row"
	"github.com/lestrrat-go/rasql/schema"
)

type UsersRow struct {
	ID    int64
	Email string
}

// ScanRow scans each result column directly into its field.
func (r *UsersRow) ScanRow(src row.ScanSource) error {
	return src.Scan(&r.ID, &r.Email)
}

// ScanDestinations maps result-column names to fields on r.
func (r *UsersRow) ScanDestinations(columns []string) ([]any, error) {
	destinations := make([]any, len(columns))
	var scanned uint64
	var discard any
	for index, column := range columns {
		switch column {
		case "id":
			if scanned&(uint64(1)<<0) != 0 {
				return nil, fmt.Errorf("duplicate result column %q", column)
			}
			scanned |= uint64(1) << 0
			destinations[index] = &r.ID
		case "email":
			if scanned&(uint64(1)<<1) != 0 {
				return nil, fmt.Errorf("duplicate result column %q", column)
			}
			scanned |= uint64(1) << 1
			destinations[index] = &r.Email
		default:
			destinations[index] = &discard
		}
	}
	return destinations, nil
}

// ColumnValue returns the value of the named column.
func (r UsersRow) ColumnValue(name string) (any, bool) {
	switch name {
	case "id":
		return r.ID, true
	case "email":
		return r.Email, true
	}
	return nil, false
}

// UsersTable is the generated table type for the "users" table.
type UsersTable struct {
	rasql.Table[UsersRow]
	ID    query.ColumnRef
	Email query.ColumnRef
}

func newUsersTable(table rasql.Table[UsersRow]) UsersTable {
	return UsersTable{
		Table: table,
		ID:    rasql.MustColumn(table, "id"),
		Email: rasql.MustColumn(table, "email"),
	}
}

var usersTable = newUsersTable(rasql.MustTableOf[UsersRow](schema.MustTableDef("users",
	schema.Integer("id"),
	schema.Text("email"),
	schema.PrimaryKey("id"),
)))

// Users returns the descriptor for the "users" table.
func Users() UsersTable {
	return usersTable
}

// As returns the table under alias, with every column rebound to it.
func (t UsersTable) As(alias string) (UsersTable, error) {
	aliased, err := rasql.As(t.Table, alias)
	if err != nil {
		return UsersTable{}, err
	}
	return newUsersTable(aliased), nil
}
```
source: [examples/store/users_gen.go](https://github.com/lestrrat-go/rasql/blob/main/examples/store/users_gen.go)
<!-- END INCLUDE -->

`store.Users()` returns a `store.UsersTable`, ready for `rasql.SelectFrom`, `rasql.Insert`, and `rasql.Update` because it embeds `rasql.Table[store.UsersRow]`. Its column fields are what the typed builders take: `store.Users().ID` is a `query.ColumnRef`, so `WhereEqual(users.ID, 1)` cannot name a column the table does not have. `store.Users().Ref()` gives the `query.TableRef` the lower-level API takes.

### Generated relationships

When the input contains tables with a single-column foreign key to another
generated table's single-column primary key, `rasqlgen` emits a typed
relationship in both directions. The foreign-key column must be non-null and
must use the same supported Go key type as the referenced primary key.

For `orders.user_id REFERENCES users(id)`, `store.Orders().User()` returns
the belongs-to relation and `store.Users().Orders()` returns its inverse
has-many relation. Call each relation's `Load` method with the `rasql.DB` and
the already-fetched rows to perform the batched eager load.

Each relation also has `Join()`, which returns the existing `query.Join` value
for use with `rasql.SelectFrom` or `rasql.DecodeFrom`. `Load` uses one `IN`
query for the supplied rows and returns a map keyed by the primary or foreign
key, so callers can attach the results without an N+1 query loop. Empty input
returns an empty map without touching the database.

For a self-referential table, the generated relation aliases its joined side
and rebinds the relation keys, so `Join()` can be used directly without a
manual `As` call.

This first slice intentionally does not generate relations for composite
foreign keys, nullable foreign keys, non-primary unique targets, many-to-many
join tables, polymorphic associations, nested preloading, or keys whose Go
type is not comparable. Those cases remain available through the existing
explicit `query.Join` and typed builder APIs.

The descriptor is a package-level variable, which Go cannot mark constant, so it stays unexported and only the accessor is exported. Importing code therefore cannot swap the descriptor out from under the rest of the program.

### The mapping methods

A generated row type carries no `rasql` tags. The generator already knows which column feeds which field, so it writes that mapping as Go code rather than as a string for reflection to parse at run time. Each method satisfies one interface:

| Interface | Method | Used by |
| --- | --- | --- |
| `row.Scanner` | `ScanRow(row.ScanSource) error` | `SelectFrom`, when the builder owns the whole projection and no `Project` call narrowed it. |
| `row.DestinationScanner` | `ScanDestinations([]string) ([]any, error)` | Every other typed read: `DecodeFrom`, a narrowed `SelectFrom`, `QueryWriteAll`/`QueryWriteOne`, `QueryRendered[T]`. |
| `rasql.ColumnValuer` | `ColumnValue(name string) (any, bool)` | `rasql.Insert` and `rasql.Update`. |

A hand-written row type without these methods is mapped by `rasql` tags and snake-cased field names, as in [Getting started](01-getting-started.md) and [Schemas](02-schema.md). The read side has no by-method mapping: deleting `DecodeRow` was deliberate, and the asymmetry with the write side's `ColumnValue` is the price of keeping `row.Dynamic` out of the typed API.

A field no single column holds is not a mapping problem. Keep the raw columns as fields and compute the value in a method:

<!-- INCLUDE(examples/rasqlgen_computed_field_example_test.go#computed_field) -->
```go
type userReport struct {
	Email     string
	FirstName string `rasql:"first_name"`
	LastName  string `rasql:"last_name"`
}

func (r userReport) FullName() string {
	return r.FirstName + " " + r.LastName
}
```
source: [examples/rasqlgen_computed_field_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasqlgen_computed_field_example_test.go)
<!-- END INCLUDE -->

The method is ordinary Go and the mapping stays a plain field-to-column mapping:

<!-- INCLUDE(examples/rasqlgen_computed_field_example_test.go#computed_field_use) -->
```go
report, err := rasql.DecodeFrom[userReport](people).
	Project(query.Project(email), query.Project(first), query.Project(last)).
	One(ctx, db)
if err != nil {
	fmt.Printf("failed to query people: %s\n", err)
	return
}
fmt.Println(report.Email, report.FullName())
```
source: [examples/rasqlgen_computed_field_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasqlgen_computed_field_example_test.go)
<!-- END INCLUDE -->

Two routes stay open beyond a computed method: a field type implementing `sql.Scanner` converts one column any way the caller likes, and two fields may carry the same `rasql` tag, each with its own `sql.Scanner`. Anyone porting an old `DecodeRow` should expect a different failure mode: the struct falls through to field mapping and reports `row: column "full_name" is not present`, loudly rather than silently.

One trap is worth stating plainly. Embedding a generated row type promotes its scan methods to the outer struct, and the typed read path uses them as Go presents them:

<!-- INCLUDE(examples/rasqlgen_embedded_row_example_test.go#embedded_row) -->
```go
type userWithRole struct {
	store.UsersRow // promotes ScanRow and ScanDestinations
	Role           string
}
```
source: [examples/rasqlgen_embedded_row_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasqlgen_embedded_row_example_test.go)
<!-- END INCLUDE -->

A `userWithRole` satisfies `row.DestinationScanner` through the promoted `ScanDestinations`, which knows only the embedded row's columns. Reading one therefore fills `ID` and `Email` from the embedded `store.UsersRow`, hands `Role` no destination at all, and reports nothing: the example prints `role=""`. `ScanRow` promotes the same way and behaves the same way. Unlike the write side, the read side does not check whether the outer type declared the method or inherited it, so there is no error to notice — the wrapper simply comes back half filled. Declare `ScanDestinations` on the outer type so it places every field, give the outer type a named field instead of embedding the row, or drop the scan methods altogether by declaring your own result struct with `rasql` tags.

Embedding promotes `ColumnValue` in the same way, and the write side reads it differently. `Insert` and `Update` map a struct that embeds a `ColumnValuer`, carries `rasql` tags of its own, and declares no `ColumnValue` by those tags, because a promoted `ColumnValue` reports the embedded values and knows nothing about the tagged fields around them. A wrapper that tags nothing is still mapped by its promoted `ColumnValue`. Declaring `ColumnValue` on the outer type maps such a wrapper by method again.

### What the column fields catch

A column named by a string is checked when the statement is built, so these two lines are indistinguishable until then:

<!-- INCLUDE(examples/rasqlgen_column_fields_example_test.go#string_column) -->
```go
correct := rasql.SelectFromRef(store.Users().Ref()).Select("id").WhereEqual("id", 42)
typo := rasql.SelectFromRef(store.Users().Ref()).Select("id").WhereEqual("emial", 42)
```
source: [examples/rasqlgen_column_fields_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasqlgen_column_fields_example_test.go)
<!-- END INCLUDE -->

The second one fails with `query column: table "users" has no column "emial"` when the statement is built, wherever that first happens.

The typed builder takes a `query.ColumnRef` instead, so the same typo stops at the compiler:

<!-- INCLUDE(examples/rasqlgen_column_fields_example_test.go#typed_column) -->
```go
users := store.Users()
built, err := rasql.SelectFrom(users).WhereEqual(users.ID, 42).Build(dialect.PostgreSQL())
```
source: [examples/rasqlgen_column_fields_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasqlgen_column_fields_example_test.go)
<!-- END INCLUDE -->

Writing `users.Emial` in place of `users.ID` there does not reach a build at all:

```
users.Emial undefined (type UsersTable has no field or method Emial)
```

Passing a name instead of a field does not compile either:

```
cannot use "id" (untyped string constant) as query.ColumnRef value in argument to
rasql.SelectFrom(users).WhereEqual
```

Three things make that work. The generator derives each field from the same descriptor it renders SQL from, so the field list and the table cannot drift apart. The builders accept a `query.ColumnRef` rather than a name, so there is no string left to misspell. Each field is bound to its table once, when the table value is built, which is why `As` rebuilds them and an aliased table qualifies its columns correctly.

The payoff arrives at the next migration. Drop or rename a column, regenerate, and every use of the old field stops compiling, instead of failing one query at a time in production.

Three mistakes still reach run time. A column of another table is a valid `query.ColumnRef`, so it compiles and fails when the statement is built:

```
query: where.left: references table "orders" outside the statement
```

A lookup by name is checked when it runs, since the name is only known then:

<!-- INCLUDE(examples/rasqlgen_column_fields_example_test.go#column_lookup) -->
```go
column, err := users.Column("emial")
```
source: [examples/rasqlgen_column_fields_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasqlgen_column_fields_example_test.go)
<!-- END INCLUDE -->

That one returns `query column: table "users" has no column "emial"`.

The value side of a comparison is `any`, so nothing stops a text column from being compared against a number. The database reports that one.

Names come from the table and column names. Underscore-separated parts are capitalized, and `id`, `api`, `json`, `url`, and `uuid` become `ID`, `API`, `JSON`, `URL`, and `UUID`. A nullable column becomes a pointer field on the row type. Logical types map as follows:

| Logical type | Generated Go type |
| --- | --- |
| `boolean` | `bool` |
| `integer` | `int64` |
| `integer` with `Unsigned` | `uint64` |
| `float` | `float64` |
| `text`, `uuid` | `string` |
| `bytes`, `json` | `[]byte` |
| `time` | `time.Time` |
| `decimal` | `string` |

A `boolean` column decodes from any integer value, not just 0 and 1: zero decodes as `false`, and any nonzero value decodes as `true`.

An `integer` column that sets `Unsigned` generates a `uint64` field rather than an `int64` one, because it reaches 18446744073709551615 and `int64` stops at 9223372036854775807. The generated descriptor restates `schema.Unsigned()`, so regenerating from the emitted source produces the same column instead of a signed one, and a `schema.TableDef` read through `-input` keeps it too, since it is a plain JSON boolean. See [Unsigned integer columns](02-schema.md#unsigned-integer-columns) for which engines can render such a column.

A `text` column that states a `Width` still generates a plain `string` field, but the generated descriptor restates `schema.Width(n)`, so regenerating keeps the same bound instead of an unbounded column, and a `schema.TableDef` read through `-input` keeps it too, since it is a plain JSON number. A fixed-width column restates `schema.Fixed()` alongside it, so regenerating a column inspected from a live `CHAR(n)`/`CHARACTER(n)` column keeps rendering `CHAR(n)` instead of reverting to `VARCHAR(n)`. See [Text column width](02-schema.md#text-column-width) for how MySQL and PostgreSQL inspection preserve both, and why MySQL needs a width to index such a column at all.

The command fails rather than emitting doubtful code when a table or column name cannot become a Go identifier, or when two of them would collide after conversion. A column also fails when its field name would be `Table`, `As`, `Ref`, `Column`, or `tableRow`, because those names belong to the embedded `rasql.Table` and its methods, or `ScanRow`, `ScanDestinations`, or `ColumnValue`, because those belong to the row type's own scan and mapping methods.

## `rasqlgen query`

```sh
go run github.com/lestrrat-go/rasql/cmd/rasqlgen query \
  -input queries/user_by_email.sql \
  -function UserByEmail \
  -dialect postgresql \
  -package store \
  -output internal/store/user_by_email_gen.go
```

Every flag is required. `-dialect` accepts `postgresql` (or `postgres`), `mysql`, and `sqlite`. The package, function, and bind names must be usable Go identifiers. The function name cannot be `init`, and `main` cannot be generated in package `main`. `-output` must end in `_gen.go`. `-input` is capped at 64 MiB; a larger file is rejected before it is parsed.

The input is a static template, so it holds SQL text plus `{{bind "name"}}` actions and nothing else:

<!-- INCLUDE(examples/store/user_by_email.sql) -->
```sql
SELECT id, email FROM users WHERE email = {{bind "email"}}
```
source: [examples/store/user_by_email.sql](https://github.com/lestrrat-go/rasql/blob/main/examples/store/user_by_email.sql)
<!-- END INCLUDE -->

The generated function takes one parameter per distinct bind name, in the order the names first appear, and returns the statement ready for `rasql.QueryRendered[T]`, `db.QueryRendered`, or `db.ExecRendered`:

<!-- INCLUDE(examples/store/user_by_email_gen.go) -->
```go
// Code generated by rasqlgen; DO NOT EDIT.

package store

import rasqlrender "github.com/lestrrat-go/rasql/render"

func UserByEmail(email any) (rasqlrender.Statement, error) {
	return rasqlrender.Precompiled("SELECT id, email FROM users WHERE email = $1\n", email)
}
```
source: [examples/store/user_by_email_gen.go](https://github.com/lestrrat-go/rasql/blob/main/examples/store/user_by_email_gen.go)
<!-- END INCLUDE -->

The SQL is compiled at generation time, so nothing parses the template at run time and a malformed template fails the build instead of a request.

## Keeping generated files current

Put the command in a `go:generate` line beside the package it writes into. The sample application in this repository generates its descriptors that way, applying its migrations to a throwaway SQLite database first so the generated source follows the migrations rather than a second copy of the schema:

<!-- INCLUDE(sample/taskboard/internal/store/generate.go#go_generate) -->
```go
//go:generate go -C ../../../.. run ./cmd/rasqlmigrate apply -dir sample/taskboard/migrations/sqlite -dialect sqlite -dsn sample/taskboard/internal/store/.taskboard-schema.db
//go:generate go -C ../../../.. run ./cmd/rasqlgen schema -dsn sample/taskboard/internal/store/.taskboard-schema.db -dialect sqlite -table members -table projects -table tasks -package store -output sample/taskboard/internal/store
```
source: [sample/taskboard/internal/store/generate.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/generate.go)
<!-- END INCLUDE -->

`rasqlgen` never writes a generated file in place. It writes a temporary file beside each table or query file and renames it over the destination only after the write succeeds, so a failed run leaves an existing file untouched. Existing files keep their permission bits and sticky bit. On Unix platforms that replacement is atomic. Schema output directories must already exist; a symbolic link to a directory is allowed.

Then `go generate ./...` refreshes everything. Because output is deterministic, a CI job can regenerate and fail when `git diff` is not empty, which catches a generated file that drifted from its source.
