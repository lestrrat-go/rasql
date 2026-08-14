# `rasqlgen`

`rasqlgen` writes Go source: table descriptors from a database or a Go schema package, and query functions from [static templates](05-templates.md). Its output is deterministic, so regenerating unchanged input produces identical files and an accidental edit shows up in review. That holds for `-dsn`; with `-source` (below), output stays only as deterministic as the user's own `Tables` function, so a `Tables` that reads the clock or ranges a map produces different output per run.

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
| `-source` | Directory of a Go package exporting `func Tables() []schema.TableDef`, instead of `-dsn`. |
| `-table` | Table to generate; repeat it for each table. Required with `-dsn`; filters a package from `-source`. Passing the same table name twice is an error. |
| `-dialect` | Dialect and driver for `-dsn`, defaulting to `postgresql`. |
| `-timeout` | Deadline for `-dsn` metadata inspection, defaulting to 30s. The deadline does not apply to `-source`, but every `schema` invocation rejects a zero or negative value. |
| `-package` | Package name for the generated files. Required. |
| `-output` | Existing directory for generated files. Required. |

Supply exactly one of `-dsn` or `-source`. Direct inspection supports PostgreSQL, MySQL, and SQLite; the command bundles their `pgx`, `mysql`, and `sqlite` drivers, so nothing needs importing to use `-dsn`. PostgreSQL inspection preserves supported columns, primary keys, named unique constraints, checks, indexes (recording a non-default access method such as `gin` in `IndexDef.Method`, generated as `schema.IndexMethod("gin")`, a partial index's `WHERE` predicate in `IndexDef.Predicate`, and an expression index's key text in `IndexDef.Expressions`), and foreign keys, including exact decimal columns (`NUMERIC`/`DECIMAL`), which generate a Go `string` field and whose generated descriptor restates the column's `Precision` and `Scale`. MySQL inspection preserves supported columns, primary keys, and indexes, likewise recording a non-default index type such as `FULLTEXT` and a functional index's key text in `IndexDef.Expressions`; SQLite inspection currently preserves supported columns and primary keys. <!-- MySQL includes indexes here; SQLite does not, and no later clause contradicts that distinction. --> A generated `schema.Decimal` call restates precision and scale positionally, and states scale even when it is `0`, because the zero value of `schema.DecimalScale` means no scale was stated and `TableDef.Validate` rejects a decimal column that states none. It reports an error when an inspected type or constraint has metadata that the schema descriptor cannot reproduce, when an index has metadata beyond its method, predicate, and key expressions that the descriptor cannot reproduce (a descending key or an included column, for example), and when a PostgreSQL column is a bare, unconstrained `NUMERIC`, since PostgreSQL reports no precision for it to record. A non-default index method, a partial index's predicate, and an expression index's key text are all describable, not yet renderable: see [Index methods](02-schema.md#index-methods) and [Partial and expression indexes](02-schema.md#partial-and-expression-indexes).

`schema` writes one file per table, named `<table>_gen.go` in lowercase, plus two files shared by the whole package: `schema_gen.go`, holding every table's runtime descriptor, and `schema_gen_test.go`, a generated test that validates them. The example writes `users_gen.go`, `orders_gen.go`, `schema_gen.go`, and `schema_gen_test.go` in `internal/store`. A rerun into the same output directory that would leave one of those per-table files behind without rewriting it is refused rather than written, on the terms [Keeping generated files current](#keeping-generated-files-current) states.

When selected tables contain supported foreign keys, `rasqlgen` also emits typed relationship descriptors in those files. It uses the complete selected-table set to connect each generated file to its target, so a relationship method is emitted only when the target table is selected too. See [Relationships](02-schema.md#relationships) for the supported slice and its eager-loading behavior.

`-dsn` reads every requested table in one transaction and commits it after the last read, so a migration that commits partway through cannot split the generated descriptor across two schema versions. It requests repeatable-read and read-only modes where the driver supports them. `-timeout` bounds that whole transaction, from opening it through the last metadata query; a server that accepts the connection but never answers cancels the command instead of hanging it indefinitely.

Live MySQL inspection requires metadata privileges that expose every column in the selected table. MySQL filters `information_schema.columns` by column privileges, so a partial grant can otherwise produce an incomplete baseline; the inspector cross-checks the count against `SHOW CREATE TABLE` and returns `inspect.ErrIncompleteMetadata` instead. Grant table- or database-level `SELECT` (or equivalent full column visibility) before using MySQL live inspection or `diff-live`.

### A schema package as the source

`-source` takes a second kind of input: a directory holding a Go package that exports exactly one function, `func Tables() []schema.TableDef`. The schema package is input only. The generated code never imports it, so it can be dropped from a production build.

<!-- INCLUDE(examples/schemasource/tables.go#schema_source_tables) -->
```go
import "github.com/lestrrat-go/rasql/schema"

func Tables() []schema.TableDef {
	return []schema.TableDef{
		schema.MustTableDef("users",
			schema.Integer("id"),
			schema.Text("email", schema.Width(255)),
			schema.PrimaryKey("id"),
		),
	}
}
```
source: [examples/schemasource/tables.go](https://github.com/lestrrat-go/rasql/blob/main/examples/schemasource/tables.go)
<!-- END INCLUDE -->

```sh
go run github.com/lestrrat-go/rasql/cmd/rasqlgen schema \
  -source ./internal/tables \
  -package store \
  -output internal/store
```

During a run, `rasqlgen` writes a temporary `package main` into the user's own module, runs it with `go run` from the module root, and deletes it again on the success path, the failure path, and on an interrupt. `rasqlgen` handles SIGINT and SIGTERM for the length of a `-source` run rather than leaving them to end the process where no cleanup can run; the directory is removed first, and the command then ends on that same signal, so a caller still sees the conventional status for an interrupted run rather than an ordinary failure. The directory name begins with a dot, so `go list ./...`, `go build ./...`, and similar `./...` patterns skip it while it exists.

The temporary program has to live inside the module: a schema package under `internal/`, which is where a schema package usually lives, cannot be imported from outside its own module. A program run from outside the module tree is rejected with `use of internal package ... not allowed`, so the temporary directory is created under the module root rather than, say, the system temp directory.

`-timeout` does not bound a `-source` run; a `go run` on a cold module cache can take far longer than the 30s default, and there is no separate deadline for it.

`-source` runs `go run`, which may need to fetch a module on a cold cache, so generation in this mode is not reliably offline. `-dsn` is unaffected by that particular issue, but it needs a reachable database of its own, so no `schema` mode is reliably offline end to end.

A vendored module needs one extra step. `go run` under `-mod=vendor` only sees what is already vendored, and a schema package that imports only `github.com/lestrrat-go/rasql/schema` does not pull in `github.com/lestrrat-go/rasql/generate`, which the temporary program also imports. Add a blank import to the schema package, `import _ "github.com/lestrrat-go/rasql/generate"`, and re-run `go mod vendor`. Without it, `go run` reports `import lookup disabled by -mod=vendor`.

A user who would rather own the program entirely can skip `rasqlgen` for this step and write the same few lines under a `//go:generate go run ./gen` line, or behind a `//go:build ignore` tag invoked directly:

<!-- INCLUDE(examples/schemasource/gen/main.go#schema_source_program) -->
```go
import (
	"fmt"
	"os"

	"github.com/lestrrat-go/rasql/examples/schemasource"
	"github.com/lestrrat-go/rasql/generate"
)

func main() {
	if err := generate.WritePackage("store", "internal/store", schemasource.Tables()...); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write schema package: %s\n", err)
		os.Exit(1)
	}
}
```
source: [examples/schemasource/gen/main.go](https://github.com/lestrrat-go/rasql/blob/main/examples/schemasource/gen/main.go)
<!-- END INCLUDE -->

`go generate` runs that directive with the schema package's own directory as the working directory, so the relative `internal/store` above names a directory inside it, and like `-output` it has to exist before the program runs. Reporting the failure is not enough on its own: a step that printed the error and returned would exit 0, and a `go generate` run would look successful while producing nothing, so the program exits nonzero instead.

Both routes call the same exported entry point, `generate.WritePackage(packageName, directory string, tables ...schema.TableDef) error`.

### What it generates

For a `users` table of `id` and `email` the per-table file declares a row type with its scan methods and its column-value method, a table type with one accessor method per column, and a table accessor:

The row type is named `<Table>Row` by default, so `users` generates `UsersRow` below. A descriptor that sets `schema.RowNamed` (or `TableDef.RowName` directly) generates that name instead, and the emitted `schema_gen.go` descriptor restates `RowName`, so regenerating from the emitted source keeps the stated name rather than reverting to the default. `RowName` reaches the generator from a Go table definition; `-dsn` inspection can never carry it, since a live database has no opinion about Go names.

<!-- INCLUDE(examples/store/users_gen.go) -->
```go
// Code generated by rasqlgen; DO NOT EDIT.

package store

import (
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/query"
)

type UsersRow struct {
	ID    int64
	Email string
}

// ScanRow scans each result column directly into its field.
func (r *UsersRow) ScanRow(src rasql.ScanSource) error {
	return src.Scan(&r.ID, &r.Email)
}

// ScanDestinations maps result-column names to fields on r.
func (r *UsersRow) ScanDestinations(columns []string) ([]any, error) {
	const (
		scanIndexID = iota
		scanIndexEmail
	)
	destinations := make([]any, len(columns))
	scanned := rasql.NewScanMask(2)
	var discard any
	for index, column := range columns {
		switch column {
		case "id":
			if !scanned.Mark(scanIndexID) {
				return nil, fmt.Errorf("duplicate result column %q", column)
			}
			destinations[index] = &r.ID
		case "email":
			if !scanned.Mark(scanIndexEmail) {
				return nil, fmt.Errorf("duplicate result column %q", column)
			}
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
}

// ID returns a reference to the "id" column.
func (t UsersTable) ID() query.ColumnRef { return rasql.ColumnOf(t.Table, "id") }

// Email returns a reference to the "email" column.
func (t UsersTable) Email() query.ColumnRef { return rasql.ColumnOf(t.Table, "email") }

// Users returns the descriptor for the "users" table.
func Users() UsersTable {
	return usersTable
}

// As returns the table under alias.
func (t UsersTable) As(alias string) (UsersTable, error) {
	aliased, err := rasql.As(t.Table, alias)
	if err != nil {
		return UsersTable{}, err
	}
	return UsersTable{Table: aliased}, nil
}
```
source: [examples/store/users_gen.go](https://github.com/lestrrat-go/rasql/blob/main/examples/store/users_gen.go)
<!-- END INCLUDE -->

The package's `schema_gen.go` holds every table's runtime descriptor:

<!-- INCLUDE(examples/store/schema_gen.go) -->
```go
// Code generated by rasqlgen; DO NOT EDIT.

package store

import (
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/schema"
)

var usersDef = schema.TableDef{
	Name: "users",
	Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "email", Type: schema.TextType{}},
	},
	PrimaryKey: []string{"id"},
}

var usersTable = UsersTable{rasql.TableFrom[UsersRow](usersDef)}

// UsersDef returns a copy of the descriptor for the "users" table.
func UsersDef() schema.TableDef { return usersDef.Clone() }
```
source: [examples/store/schema_gen.go](https://github.com/lestrrat-go/rasql/blob/main/examples/store/schema_gen.go)
<!-- END INCLUDE -->

`store.Users()` returns a `store.UsersTable`, ready for `rasql.SelectFrom`, `rasql.Insert`, and `rasql.Update` because it embeds `rasql.Table[store.UsersRow]`. Its column accessors are what the typed builders take: `store.Users().ID()` is a `query.ColumnRef`, so `WhereEqual(users.ID(), 1)` cannot name a column the table does not have. `store.Users().Ref()` gives the `query.TableRef` the lower-level API takes, and `store.UsersDef()` hands back a copy of the descriptor.

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

### The mapping and scan methods

A generated row type carries no `rasql` tags. The generator already knows which column feeds which field, so it writes that mapping as Go code rather than as a string for reflection to parse at run time. Each method satisfies one interface:

| Interface | Method | Used by |
| --- | --- | --- |
| `rasql.Scanner` | `ScanRow(rasql.ScanSource) error` | Every typed select whose builder owns the complete table projection. |
| `rasql.DestinationScanner` | `ScanDestinations(columns []string) ([]any, error)` | Every typed select over a partial or reordered projection, and every typed write that reads a `RETURNING` clause. |
| `rasql.ColumnValuer` | `ColumnValue(name string) (any, bool)` | `rasql.Insert` and `rasql.Update`. |

`ScanRow` runs when the builder states the whole table projection, so the column order is known before the query runs. `ScanDestinations` runs whenever it is not: a projected subset, a reordered result, or a `RETURNING` clause. A row type that declares neither is mapped by its `rasql` tags and snake-cased field names.

The generated `ScanDestinations` rejects a result set that names the same column twice, rather than scanning it into the same field twice. It tracks which columns it has already placed in a [`rasql.ScanMask`](https://pkg.go.dev/github.com/lestrrat-go/rasql#ScanMask), built with `rasql.NewScanMask(columnCount)` and marked one column at a time with `Mark`, which reports `false` for a column already placed. A hand-written `ScanDestinations` can use the same type for the same check.

A hand-written row type without these methods is mapped by `rasql` tags and snake-cased field names, as in [Getting started](01-getting-started.md) and [Schemas](02-schema.md). The read side has no by-method mapping: deleting `DecodeRow` was deliberate, and the asymmetry with the write side's `ColumnValue` is the price of keeping `dynamic.Row` out of the typed API.

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

A `userWithRole` satisfies `rasql.DestinationScanner` through the promoted `ScanDestinations`, which knows only the embedded row's columns. Reading one therefore fills `ID` and `Email` from the embedded `store.UsersRow`, hands `Role` no destination at all, and reports nothing: the example prints `role=""`. `ScanRow` promotes the same way and behaves the same way. Unlike the write side, the read side does not check whether the outer type declared the method or inherited it, so there is no error to notice — the wrapper simply comes back half filled. Declare `ScanDestinations` on the outer type so it places every field, give the outer type a named field instead of embedding the row, or drop the scan methods altogether by declaring your own result struct with `rasql` tags.

Embedding promotes `ColumnValue` in the same way, and the write side reads it differently. `Insert` and `Update` map a struct that embeds a `ColumnValuer`, carries `rasql` tags of its own, and declares no `ColumnValue` by those tags, because a promoted `ColumnValue` reports the embedded values and knows nothing about the tagged fields around them. A wrapper that tags nothing is still mapped by its promoted `ColumnValue`. Declaring `ColumnValue` on the outer type maps such a wrapper by method again.

### What the column accessors catch

A column named by a string is checked when the statement is built, so these two lines are indistinguishable until then:

<!-- INCLUDE(examples/rasqlgen_column_fields_example_test.go#string_column) -->
```go
correct := dynamic.SelectFrom(store.Users().Ref()).Select("id").WhereEqual("id", 42)
typo := dynamic.SelectFrom(store.Users().Ref()).Select("id").WhereEqual("emial", 42)
```
source: [examples/rasqlgen_column_fields_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasqlgen_column_fields_example_test.go)
<!-- END INCLUDE -->

The second one fails with `query column: table "users" has no column "emial"` when the statement is built, wherever that first happens.

The typed builder takes a `query.ColumnRef` instead, so the same typo stops at the compiler:

<!-- INCLUDE(examples/rasqlgen_column_fields_example_test.go#typed_column) -->
```go
users := store.Users()
built, err := rasql.SelectFrom(users).WhereEqual(users.ID(), 42).Build(dialect.PostgreSQL())
```
source: [examples/rasqlgen_column_fields_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasqlgen_column_fields_example_test.go)
<!-- END INCLUDE -->

Writing `users.Emial()` in place of `users.ID()` there does not reach a build at all:

```
users.Emial undefined (type UsersTable has no field or method Emial)
```

Passing a name instead of an accessor call does not compile either:

```
cannot use "id" (untyped string constant) as query.ColumnRef value in argument to
rasql.SelectFrom(users).WhereEqual
```

Three things make that work. The generator derives each accessor from the same descriptor it renders SQL from, so the accessor list and the table cannot drift apart. The builders accept a `query.ColumnRef` rather than a name, so there is no string left to misspell. Each accessor reads the table value it is called on, rather than a value bound once when the table was built, which is why an aliased table's accessors qualify its columns correctly with nothing to rebind.

The payoff arrives at the next migration. Drop or rename a column, regenerate, and every call to the old accessor method stops compiling, instead of failing one query at a time in production.

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

An `integer` column that sets `Unsigned` generates a `uint64` field rather than an `int64` one, because it reaches 18446744073709551615 and `int64` stops at 9223372036854775807. The generated descriptor restates `schema.Unsigned()`, so regenerating from the emitted source produces the same column instead of a signed one. See [Unsigned integer columns](02-schema.md#unsigned-integer-columns) for which engines can render such a column.

A `text` column that states a `Width` still generates a plain `string` field, but the generated descriptor restates `schema.Width(n)`, so regenerating keeps the same bound instead of an unbounded column. A fixed-width column restates `schema.Fixed()` alongside it, so regenerating a column inspected from a live `CHAR(n)`/`CHARACTER(n)` column keeps rendering `CHAR(n)` instead of reverting to `VARCHAR(n)`. See [Text column width](02-schema.md#text-column-width) for how MySQL and PostgreSQL inspection preserve both, and why MySQL needs a width to index such a column at all.

The command fails rather than emitting doubtful code when a table or column name cannot become a Go identifier, or when two of them would collide after conversion. A column also fails when its generated name would be `Table`, `As`, `Ref`, `Column`, or `tableRow`, because its column accessor method would collide with the embedded `rasql.Table` and its methods, or `ScanRow`, `ScanDestinations`, or `ColumnValue`, because its row type field would collide with the row type's own scan and mapping methods. A table also fails when its accessor would spell the fixed function name `schema_gen_test.go` declares, since one run would otherwise write that identifier into two files of the same package; the error names the identifier. A stated `RowName` fails the same way when it is not a valid, exported Go identifier, when it names one of those reserved methods, or when it collides with another generated name, such as the table's own accessor or table type, another table's row type, a relationship type name, or that fixed function name.

## `rasqlgen query`

```sh
go run github.com/lestrrat-go/rasql/cmd/rasqlgen query \
  -input queries/user_by_email.sql \
  -function UserByEmail \
  -dialect postgresql \
  -package store \
  -output internal/store/user_by_email_gen.go
```

Every flag is required. `-dialect` accepts `postgresql` (or `postgres`), `mysql`, and `sqlite`. The package, function, and bind names must be usable Go identifiers. The function name cannot be `init`, and `main` cannot be generated in package `main`. `-output` must end in `_gen.go`, and must name a file `rasqlgen` itself wrote if it exists at all, on the terms [Keeping generated files current](#keeping-generated-files-current) states. `-input` is capped at 64 MiB; a larger file is rejected before it is parsed.

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

`rasqlgen` replaces only files it wrote itself. Every file it writes opens with the line `// Code generated by rasqlgen; DO NOT EDIT.`, and a destination that already exists must open with that same line. A destination holding anything else stops the run with an error naming the file, so a hand-written file that happens to sit where generated output lands is never destroyed. A `schema` run checks every file it is about to write before it writes the first one, so that refusal cannot leave a package with some files regenerated and the rest as they were. Regenerating output from an earlier run is unaffected, since that output carries the line.

One `schema` run has to write every `<table>_gen.go` file the package already holds. The run rewrites `schema_gen.go` from the tables it was given, so a table left out loses its descriptor there while the `<table>_gen.go` file generated for it earlier stays behind reading it, and the package stops compiling. A run that would end that way refuses before writing anything, naming the file it would strand: generate every table the package needs in one run, or delete that file first. `rasqlgen` never deletes a file itself. A query file generated into the same directory is unaffected, since it declares no descriptor and reads none.

What the run has to match is the file an earlier table generated, not the name it was generated under. A file is named after the table in lowercase, so a table renamed only in its case — `Users` to `users` — still generates `users_gen.go` and still declares `usersTable`: the rerun rewrites what is already there and is allowed. A rename that changes the file is not, even when the two names share a descriptor value: `APIKeys` generates `apikeys_gen.go` and `api_keys` generates `api_keys_gen.go`, so a rerun under the second name would leave both files declaring the same types. It is refused with the older file named, and deleting that file is what lets the rename through.

`rasqlgen` never writes a generated file in place. It writes a temporary file beside each table or query file and renames it over the destination only after the write succeeds, so a failed run leaves an existing file untouched. Existing files keep their permission bits and sticky bit. On Unix platforms that replacement is atomic. Schema output directories must already exist; a symbolic link to a directory is allowed.

Then `go generate ./...` refreshes everything. Because output is deterministic with `-dsn`, a CI job can regenerate and fail when `git diff` is not empty, which catches a generated file that drifted from its source; a `-source` `Tables` function that is not itself deterministic can defeat that check.
