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

For a `users` table the file declares a row type with its two mapping methods, a table type with one field per column, and a table accessor:

```go
// Code generated by rasqlgen; DO NOT EDIT.

package store

type UsersRow struct {
	ID    int64
	Email string
}

// DecodeRow assigns each result column to its field.
func (r *UsersRow) DecodeRow(src row.Dynamic) error {
	if err := row.Assign(src, "id", &r.ID); err != nil {
		return err
	}
	return row.Assign(src, "email", &r.Email)
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

var usersTable = newUsersTable(rasql.MustTableOf[UsersRow](schema.MustTable("users" /* … */)))

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

`store.Users()` returns a `store.UsersTable`, ready for `rasql.SelectFrom`, `rasql.Insert`, and `rasql.Update` because it embeds `rasql.Table[store.UsersRow]`. Its column fields are what the typed builders take: `store.Users().ID` is a `query.ColumnRef`, so `WhereEqual(users.ID, 1)` cannot name a column the table does not have. `store.Users().Ref()` gives the `query.TableRef` the lower-level API takes.

### Generated relationships

When the input contains tables with a single-column foreign key to another
generated table's single-column primary key, `rasqlgen` emits a typed
relationship in both directions. The foreign-key column must be non-null and
must use the same supported Go key type as the referenced primary key.

For `orders.user_id REFERENCES users(id)`, `store.Orders().User()` returns
the belongs-to relation and `store.Users().Orders()` returns its inverse
has-many relation. Call each relation's `Load` method with the executor and
the already-fetched rows to perform the batched eager load.

Each relation also has `Join()`, which returns the existing `query.Join` value
for use with `rasql.SelectFrom` or `rasql.DecodeFrom`. `Load` uses one `IN`
query for the supplied rows and returns a map keyed by the primary or foreign
key, so callers can attach the results without an N+1 query loop. Empty input
returns an empty map without touching the executor.

For a self-referential table, the generated relation aliases its joined side
and rebinds the relation keys, so `Join()` can be used directly without a
manual `As` call.

This first slice intentionally does not generate relations for composite
foreign keys, nullable foreign keys, non-primary unique targets, many-to-many
join tables, polymorphic associations, nested preloading, or keys whose Go
type is not comparable. Those cases remain available through the existing
explicit `query.Join` and typed builder APIs.

The descriptor is a package-level variable, which Go cannot mark constant, so it stays unexported and only the accessor is exported. Importing code therefore cannot swap the descriptor out from under the rest of the program.

### The two mapping methods

A generated row type carries no `rasql` tags. The generator already knows which column feeds which field, so it writes that mapping as Go code rather than as a string for reflection to parse at run time. Each method satisfies one interface:

| Interface | Method | Used by |
| --- | --- | --- |
| `row.Decoder` | `DecodeRow(row.Dynamic) error` | `row.Decode`, and through it every typed select. |
| `rasql.ColumnValuer` | `ColumnValue(name string) (any, bool)` | `rasql.Insert` and `rasql.Update`. |

`row.Decode` looks for `DecodeRow` first and falls back to tags and snake-cased field names, and `Insert` and `Update` look for `ColumnValue` the same way. Neither direction follows a mapping method promoted from an embedded field blindly, which the trap below covers. Nothing about hand-written row types changes: tags stay the documented default for them, as in [Getting started](01-getting-started.md) and [Schemas](02-schema.md). Writing both methods by hand states the mapping three times — once in the fields, once in `DecodeRow`, once in `ColumnValue` — and nothing checks that the three agree, which is a job for the generator rather than for a person.

`DecodeRow` is still the escape hatch for a mapping a tag cannot express, because it is ordinary code and can do more than name a column. A field computed from two columns is the usual case:

```go
type userReport struct {
	Email    string
	FullName string
}

func (r *userReport) DecodeRow(src row.Dynamic) error {
	if err := row.Assign(src, "email", &r.Email); err != nil {
		return err
	}
	var first, last string
	if err := row.Assign(src, "first_name", &first); err != nil {
		return err
	}
	if err := row.Assign(src, "last_name", &last); err != nil {
		return err
	}
	r.FullName = first + " " + last
	return nil
}
```

One trap is worth stating plainly. Embedding a row type promotes its `DecodeRow` to the outer struct, so the outer struct satisfies `row.Decoder` through a method that knows nothing about the fields added around it:

```go
type userWithRole struct {
	store.UsersRow        // promotes DecodeRow
	Role           string
}
```

`row.Decode` does not follow that promotion blindly. It maps a struct that embeds a `row.Decoder`, declares mappable fields of its own, and declares no `DecodeRow` by those fields, because a promoted `DecodeRow` fills the embedded fields and knows nothing about the ones declared around them. Decoding a `userWithRole` therefore maps the embedded `store.UsersRow` like any other field, and fails with `row: column "users_row" is not present` instead of leaving `Role` at its zero value without reporting anything. Declare a `DecodeRow` on the outer type that calls the embedded one and then assigns the extra fields, or give the outer type its own named field instead of embedding. Tagging the embedded field `rasql:"-"` maps the wrapper by its own fields alone and leaves the embedded ones zero. A wrapper that declares no field of its own is still mapped by its promoted `DecodeRow`.

Embedding promotes `ColumnValue` in the same way, and the write side reads it the same way. `Insert` and `Update` map a struct that embeds a `ColumnValuer`, carries `rasql` tags of its own, and declares no `ColumnValue` by those tags, because a promoted `ColumnValue` reports the embedded values and knows nothing about the tagged fields around them. A wrapper that tags nothing, such as the `userWithRole` above, is still mapped by its promoted `ColumnValue`. Declaring the mapping method on the outer type — `DecodeRow` for reads, `ColumnValue` for writes — maps such a wrapper by method again, because Go dispatches to the declared method rather than to the promoted one, and the tags may stay or go.

Which fields put a wrapper on the field path is where the two directions still differ. The read side maps tagged fields and untagged exported ones, so any exported field of its own is enough — embedded or named, as long as it is not the embedded field supplying the promoted `DecodeRow` — while the write side reads tags only.

### What the column fields catch

A column named by a string is checked when the query runs, so these two lines are indistinguishable until then:

```go
rasql.SelectFromRef(store.Users().Ref()).WhereEqual("id", 42)
rasql.SelectFromRef(store.Users().Ref()).WhereEqual("emial", 42)
```

The second one fails on execution with `rasql: render SELECT: query column: table "users" has no column "emial"`, wherever the query first runs.

The typed builder takes a `query.ColumnRef` instead, so the same typo stops at the compiler:

```go
users := store.Users()
rasql.SelectFrom(users).WhereEqual(users.ID, 42)    // builds
rasql.SelectFrom(users).WhereEqual(users.Emial, 42) // does not
```

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

```go
column, err := users.Column("emial") // query column: table "users" has no column "emial"
```

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

The command fails rather than emitting doubtful code when a table or column name cannot become a Go identifier, or when two of them would collide after conversion. A column also fails when its field name would be `Table`, `As`, `Ref`, `Column`, or `tableRow`, because those names belong to the embedded `rasql.Table` and its methods, or `DecodeRow` or `ColumnValue`, because those belong to the row type's own mapping methods.

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

```sql
SELECT id, email FROM users WHERE email = {{bind "email"}}
```

The generated function takes one parameter per distinct bind name, in the order the names first appear, and returns the statement ready for `rasql.QueryRendered[T]`, `client.QueryRendered`, or `client.ExecRendered`:

```go
func UserByEmail(email any) (render.Statement, error)
```

The SQL is compiled at generation time, so nothing parses the template at run time and a malformed template fails the build instead of a request.

## Keeping generated files current

Put the command in a `go:generate` line beside the package it writes into:

```go
//go:generate go run github.com/lestrrat-go/rasql/cmd/rasqlgen schema -input schema.json -package store -output .
```

`rasqlgen` never writes a generated file in place. It writes a temporary file beside each table or query file and renames it over the destination only after the write succeeds, so a failed run leaves an existing file untouched. Existing files keep their permission bits and sticky bit. On Unix platforms that replacement is atomic. Schema output directories must already exist; a symbolic link to a directory is allowed.

Then `go generate ./...` refreshes everything. Because output is deterministic, a CI job can regenerate and fail when `git diff` is not empty, which catches a generated file that drifted from its source.
