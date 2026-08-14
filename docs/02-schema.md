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
	// with schema.MustTableDef. A column constructor such as schema.Integer and
	// a constraint constructor such as schema.PrimaryKey each return a
	// schema.TableOption, so they may appear in any order: PrimaryKey names
	// "id" below before Integer declares it, and the assembled descriptor is
	// the same either way. The same descriptor can later supply a reusable
	// query.TableRef or generate DDL.
	//
	// RowNamed states the Go row type rasqlgen generates for the table: here
	// it makes the row type User instead of the default UsersRow, so calling
	// code reads store.User rather than store.UsersRow. Like RelationshipNamed
	// below, it is a code-generation hint only — rasqlgen reads it, but
	// nothing else in rasql does, and it never appears in rendered SQL.
	users := schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.Text("nickname", schema.Nullable()),
		schema.Time("created_at", schema.Default("CURRENT_TIMESTAMP")),
		schema.Decimal("balance", 19, 4),
		schema.PrimaryKey("id"),
		schema.Unique("email"),
		schema.Index("users_email_idx", "email"),
		schema.Check("balance >= 0"),
		schema.RowNamed("User"),
	)

	// A foreign key's Named, References, and OnDelete options configure the
	// constraint itself. RelationshipNamed additionally derives the belongs-to
	// schema.RelationshipDef that rasqlgen would otherwise name on its own
	// from the local column, letting the generated method read
	// orders.Buyer() rather than orders.Customer().
	orders := schema.MustTableDef("orders",
		schema.Integer("id"),
		schema.Integer("customer_id"),
		schema.PrimaryKey("id"),
		schema.ForeignKey("customer_id",
			schema.Named("orders_customer_fkey"),
			schema.References("customers", "id"),
			schema.OnDelete(schema.Cascade),
			schema.RelationshipNamed("buyer")),
	)

	fmt.Printf("%s: %d columns, primary key %v, row type %s\n", users.Name, len(users.Columns), users.PrimaryKey, users.RowName)
	fmt.Printf("%s: foreign key %s references %s, relationship %q\n",
		orders.Name, orders.ForeignKeys[0].Name, orders.ForeignKeys[0].ReferencedTable, orders.Relationships[0].Name)

	// Output:
	// users: 5 columns, primary key [id], row type User
	// orders: foreign key orders_customer_fkey references customers, relationship "buyer"
}
```
source: [examples/schema_table_definition_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/schema_table_definition_example_test.go)
<!-- END INCLUDE -->

`schema.MustTableDef` panics on an invalid descriptor and suits a table
declared once at package initialization, exactly like `rasql.MustTableOf[T]`;
`schema.NewTableDef` returns the error instead, for a descriptor assembled at
runtime. Both collect the columns and constraints each `schema.TableOption`
declares and assemble them into a `schema.TableDef` afterward, which is what
makes the order harmless: `schema.PrimaryKey("id")` may appear before
`schema.Integer("id")` declares the column it names. Both then validate the
assembled descriptor exactly as `TableDef.Validate` would validate a struct
literal, so an unknown primary key column or a duplicate constraint name is
rejected the same way either form is built.

A column constructor such as `schema.Integer` or `schema.Text` takes zero or
more `schema.ColumnOption` values: `schema.Nullable()` marks the column
nullable, `schema.Default(expr)` states its default expression,
`schema.Unsigned()` marks an integer column unsigned, and `schema.Width(n)`
states a text column's maximum number of characters; each of the latter two
is rejected on every column type it does not name. `schema.Decimal` takes
precision and scale as ordinary arguments rather than options, since
`TableDef.Validate` requires both anyway.

| Constructor | Declares |
| --- | --- |
| `schema.PrimaryKey` | The columns that uniquely identify each row. |
| `schema.Unique` / `schema.UniqueNamed` | An unnamed, or named, uniqueness requirement over columns. |
| `schema.Check` / `schema.CheckNamed` | An unnamed, or named, check constraint over an expression. |
| `schema.Index` / `schema.UniqueIndex` | A plain, or unique, secondary index over columns. |
| `schema.ForeignKey` / `schema.ForeignKeyOn` | A foreign key over one column, or over several. |
| `schema.InSchema` | The namespace qualifying the table. |
| `schema.RowNamed` | The Go type name `rasqlgen` gives the row type. |

`schema.ForeignKey` takes the single local column and `schema.ForeignKeyOn`
takes a `[]string` of them for a composite key; both take the same list of
`schema.ForeignKeyOption` values: `schema.Named` states the constraint name,
`schema.References` states the target table and columns, `schema.ReferencesIn`
does the same for a target qualified by schema, `schema.OnDelete` and
`schema.OnUpdate` state the reference actions (`schema.Cascade`,
`schema.Restrict`, `schema.SetNull`, `schema.SetDefault`, and
`schema.NoAction`), and `schema.RelationshipNamed` derives a belongs-to
`RelationshipDef` alongside it. Together these constructors cover every shape
a struct literal can express: a composite foreign key, a named unique
constraint or check, and a unique index all have an option-form constructor,
with no need to fall back
to a struct literal for any of them.

## The struct literal

`schema.TableDef` is the descriptor itself; `schema.NewTableDef` and
`schema.MustTableDef` are one way to build one. Its fields are exactly what a
`schema.TableOption` assembles behind the scenes, and they are also what
`inspect` returns from a live database and what `migrate`'s diff compares
between two descriptors, so reading a descriptor back, whether from
`inspect.Table` or from a variable holding one, means reading this struct
rather than a list of options:

| Field | Holds |
| --- | --- |
| `Schema` | The optional namespace holding the table. |
| `Name` | The table identifier. |
| `RowName` | Optional Go type name for the generated row type; empty means `<Table>Row`. |
| `Columns` | Each column, in the order it is declared. |
| `PrimaryKey` | Column names from `Columns` that identify a row. |
| `Strict` | A SQLite `STRICT` table (see [SQLite table-level options](#sqlite-table-level-options)). |
| `WithoutRowID` | A SQLite `WITHOUT ROWID` table (see [SQLite table-level options](#sqlite-table-level-options)). |
| `PrimaryKeyAutoincrement` | A SQLite `AUTOINCREMENT` primary key (see [SQLite table-level options](#sqlite-table-level-options)). |
| `PrimaryKeyOnConflict` | A SQLite primary key's `ON CONFLICT` resolution (see [SQLite table-level options](#sqlite-table-level-options)). |
| `VirtualTableModule` | A SQLite virtual table's module name (see [SQLite virtual tables](#sqlite-virtual-tables)). |
| `VirtualTableModuleArguments` | A SQLite virtual table's raw module arguments (see [SQLite virtual tables](#sqlite-virtual-tables)). |
| `UniqueConstraints` | Named or unnamed uniqueness requirements. |
| `Checks` | Check constraints. |
| `Indexes` | Secondary indexes. |
| `ForeignKeys` | References to other tables, with their update and delete actions. |
| `Relationships` | Optional named relationship metadata used by generated relationship APIs. |

A struct literal remains a fully supported way to build a `schema.TableDef`
directly, and every field takes a keyed composite literal such as
`schema.TableDef{Name: "orders", Columns: []schema.ColumnDef{...}, ...}`. An
unkeyed literal is not supported: it matches fields by position and must list
every one of them, so it is not a way to build a descriptor. Call `Validate`
before using a descriptor built this way. It reports a
`*schema.ValidationError` naming the part that is wrong, such as a primary
key that lists a column the table does not declare. Non-empty names given to
`UniqueConstraints`, `Checks`, and `ForeignKeys` must be unique across all
three, since a dialect renders them together into one `CREATE TABLE`
constraint list. `MustTableDef` and `NewTableDef` validate as well, so a
separate `Validate` call is only needed for a descriptor built at runtime
that is not immediately turned into a table.

## Unique constraint facts

`UniqueDef` carries ten facts a live database can attach to a unique constraint beyond a plain column list: `Deferrable`, `NullsNotDistinct`, `IncludeColumns`, `OnConflict`, `Keys`, `Temporal`, `StorageParameters`, `Tablespace`, `ReplicaIdentity`, and `Collations`. `inspect.Table` now records all ten instead of failing the whole table, as it used to whenever a PostgreSQL unique constraint was deferrable, used `NULLS NOT DISTINCT`, named an `INCLUDE` clause, was declared `WITHOUT OVERLAPS`, or its backing index carried a storage parameter, lived outside the default tablespace, served as the table's replica identity, or used a nondefault per-column collation, or a SQLite `UNIQUE` constraint named an `ON CONFLICT` clause, a `DESC` key, or a non-default per-key collation — the last of the PostgreSQL conditions that used to abort a sweep over a production schema outright.

`Deferrable` reuses `schema.Deferrability`, the same type `ForeignKeyDef.Deferrable` uses (see [Foreign key facts](#foreign-key-facts)). Its zero value, the empty string, means `NOT DEFERRABLE`, the only form every descriptor written before this field existed has always meant.

`NullsNotDistinct` marks a PostgreSQL 15+ `UNIQUE NULLS NOT DISTINCT` constraint, under which two `NULL`s in the constrained columns conflict with each other instead of coexisting. Its zero value, `false`, means `NULLS DISTINCT`, the SQL default and the only behavior every prior descriptor has meant.

`IncludeColumns` lists a PostgreSQL unique constraint's `INCLUDE` columns: columns carried by the constraint's backing index for covering reads without taking part in the uniqueness check itself. Its zero value, `nil`, means the constraint has no `INCLUDE` clause, which is what every `UniqueDef` written before this field existed has always meant.

`OnConflict` names a SQLite unique constraint's `ON CONFLICT` resolution as a `schema.ConflictResolution`. Its zero value, the empty string, means SQLite's own default resolution, `ABORT`, the only behavior every prior descriptor has meant — an explicit `ON CONFLICT ABORT` clause names the same zero value, since it behaves identically to no clause at all. `schema.ConflictRollback`, `schema.ConflictFail`, `schema.ConflictIgnore`, and `schema.ConflictReplace` name the other four resolutions SQLite defines.

`Keys`, when non-empty, is the full ordered list of per-key facts for a SQLite `UNIQUE` constraint that orders at least one key `DESC` or names a non-default collation on it, reusing `IndexKeyDef` — the same type `IndexDef.Keys` uses (see [Index key details](#index-key-details)) — rather than a second type with the same shape. When `Keys` is set it replaces `Columns` as the source of the constraint's key order, the same way `IndexDef.Keys` replaces `IndexDef.Columns`. `IndexKeyDef.Expression` is always a bare column name here, never expression text: SQLite's own grammar prohibits an expression inside a `UNIQUE` table constraint (only a column reference, optionally `COLLATE`d and ordered, is allowed), so `inspect.Table` still refuses a `UNIQUE` constraint if it ever encounters one anyway, on the theory that a hand-supplied definition might reach rasql's parser even though a live SQLite catalog itself never produces one. `IndexKeyDef.OperatorClass`, `.PrefixLength`, and `.NullsOrder` never come from a SQLite descriptor, the same as they never come from a SQLite `IndexDef.Keys`. Its zero value, `nil`, means every key is a plain ascending column in the default collation, described by `Columns` exactly as before this field existed. `TableDef.Validate` rejects a `UniqueDef` that sets both `Columns` and `Keys`.

`Temporal` marks a PostgreSQL 18+ temporal unique constraint declared with `WITHOUT OVERLAPS` on its last column. Its zero value, `false`, means the constraint is an ordinary, non-temporal `UNIQUE` constraint, which is what every `UniqueDef` written before this field existed has always meant.

`StorageParameters`, `Tablespace`, and `ReplicaIdentity` record the same three facts about a named unique constraint's own backing index that `IndexDef.StorageParameters`, `.Tablespace`, and `.ReplicaIdentity` record about a plain index (see [Index validity, storage, and placement facts](#index-validity-storage-and-placement-facts)): storage parameters such as `fillfactor` from a `WITH (...)` clause, a nondefault tablespace, and whether the index serves as the table's `REPLICA IDENTITY USING INDEX`. Each keeps the same zero value and the same meaning as its `IndexDef` counterpart.

`Collations` records a named unique constraint's backing index's non-default per-column collation, keyed by column name and valued by the collation exactly as the server reports it; a column missing from the map uses its own default collation. Its zero value, `nil`, means every column of the constraint uses its own default collation, which is what every `UniqueDef` written before this field existed has always meant.

All ten are describable but not yet renderable. `render.CreateTable` and the migrate diff-live path refuse to build DDL for a unique constraint naming any of them, returning a `*render.UnsupportedUniqueDeferrabilityError`, `*render.UnsupportedUniqueNullsNotDistinctError`, `*render.UnsupportedUniqueIncludeColumnsError`, `*render.UnsupportedUniqueConflictResolutionError`, `*render.UnsupportedUniqueKeyDetailsError`, `*render.UnsupportedUniqueTemporalError`, `*render.UnsupportedUniqueStorageParametersError`, `*render.UnsupportedUniqueTablespaceError`, `*render.UnsupportedUniqueReplicaIdentityError`, or `*render.UnsupportedUniqueCollationsError` that names the constraint and, where relevant, the fact it named, since rasql does not yet know how to construct anything other than a plain `UNIQUE (...)` constraint. There is no option-form constructor for any of the ten yet: today they only appear on a descriptor `inspect` produced.

SQLite inspection still refuses a `UNIQUE` constraint with an expression, collation, or ordering; that stays out of scope for now.

## SQLite table-level options

`TableDef` carries four SQLite table-level facts beyond its columns and primary key: `Strict`, `WithoutRowID`, `PrimaryKeyAutoincrement`, and `PrimaryKeyOnConflict`. `inspect.Table` now records all four instead of failing the whole table, as it used to whenever a SQLite table was `STRICT`, was `WITHOUT ROWID`, or its primary key used `AUTOINCREMENT` or named an `ON CONFLICT` clause.

`Strict` marks a SQLite `STRICT` table: every column must declare one of SQLite's strict type names, and a value that does not match a column's type is rejected instead of stored under SQLite's usual type-affinity rules. Its zero value, `false`, means the table is not `STRICT`, which is what every `TableDef` written before this field existed has always meant.

`WithoutRowID` marks a SQLite `WITHOUT ROWID` table, which stores rows keyed directly by their primary key instead of by an implicit rowid. Its zero value, `false`, means the table has the usual SQLite rowid, which is what every `TableDef` written before this field existed has always meant.

`PrimaryKeyAutoincrement` marks a SQLite primary key declared with the `AUTOINCREMENT` keyword, which changes SQLite's rowid-allocation algorithm to never reuse a rowid a deleted row once used. It lives on `TableDef` rather than on a column or on `PrimaryKey`'s own string list, because `AUTOINCREMENT` is a property of the table's single `INTEGER PRIMARY KEY` declaration rather than of any one column definition. Its zero value, `false`, means the primary key carries no `AUTOINCREMENT` keyword, which is what every `TableDef` written before this field existed has always meant.

`PrimaryKeyOnConflict` names a SQLite primary key's `ON CONFLICT` resolution, reusing `schema.ConflictResolution` — the same type `UniqueDef.OnConflict` uses (see [Unique constraint facts](#unique-constraint-facts)) — rather than a second spelling of the same five resolutions. Its zero value, the empty string, means SQLite's own default resolution, `ABORT`, the only behavior every prior descriptor has meant.

`PrimaryKeyAutoincrement` and `PrimaryKeyOnConflict` are both meaningless on a table with no primary key at all, so `TableDef.Validate` rejects either one set on a `TableDef` whose `PrimaryKey` is empty.

All four facts are describable but not yet renderable. `render.CreateTable` and the migrate diff-live path refuse to build DDL for a table naming any of them, returning a `*render.UnsupportedTableStrictError`, `*render.UnsupportedTableWithoutRowIDError`, `*render.UnsupportedPrimaryKeyAutoincrementError`, or `*render.UnsupportedPrimaryKeyConflictResolutionError` that names the table and, for the last, the conflict resolution it named, since rasql does not yet know how to construct a `STRICT` table, a `WITHOUT ROWID` table, or an `AUTOINCREMENT` or `ON CONFLICT` primary key. There is no option-form constructor for any of the four yet: today they only appear on a descriptor `inspect` produced.

PostgreSQL and MySQL have no `STRICT`, `WITHOUT ROWID`, or SQLite-style primary key `AUTOINCREMENT`/`ON CONFLICT` concept (MySQL's own `AUTO_INCREMENT` is a column-level option, unrelated to `PrimaryKeyAutoincrement`), so none of the four ever comes from a PostgreSQL or MySQL descriptor. SQLite inspection still refuses a `CREATE TABLE AS SELECT` definition and a table kind other than a plain table, a virtual table, or a virtual table's own shadow table (see [SQLite virtual tables](#sqlite-virtual-tables)); those stay out of scope for now.

## SQLite virtual tables

`TableDef.VirtualTableModule` and `.VirtualTableModuleArguments` describe a SQLite `CREATE VIRTUAL TABLE` definition, and `ColumnDef.Hidden` describes a column the table's own module declares hidden. `inspect.Table` now records all three instead of failing the whole table, as it used to whenever a table was a `CREATE VIRTUAL TABLE` definition or carried a hidden column — the kind a virtual table module declares, such as `fts5`'s own table-name column, used for `MATCH` filtering, and its `rank` column. A full-text search table is one of the most common ways a SQLite database uses `CREATE VIRTUAL TABLE`, and it always carries hidden columns of exactly this kind, so this fact is what previously stopped a `rasqlgen bootstrap` sweep from completing over a database that had one.

`VirtualTableModule` names the module a `USING` clause declares, such as `"fts5"` or `"rtree"`. Its zero value, the empty string, means the table is an ordinary table, not a virtual one, which is what every `TableDef` and every checked-in generated file written before this field existed has always meant.

`VirtualTableModuleArguments` lists the module's own arguments, exactly as the `CREATE VIRTUAL TABLE` definition wrote them, one raw text span per argument in declaration order. A module defines its own argument grammar — `fts5`'s own column-definition-like arguments, for instance, which can themselves contain `=` and quoted option values — so `inspect` does not parse into it; each argument is exactly the source text between its delimiting commas or parentheses, trimmed of leading and trailing space. Its zero value, `nil`, means the module took no arguments, or `VirtualTableModule` is itself empty. It is meaningless, and `TableDef.Validate` rejects it, without a `VirtualTableModule`.

A virtual table has no primary key, table option, unique constraint, check, index, or foreign key that this package can independently verify — those are the module's own business, not something SQLite's table catalog or `PRAGMA` output describes — so `TableDef.Validate` rejects any of `PrimaryKey`, `Strict`, `WithoutRowID`, `UniqueConstraints`, `Checks`, `Indexes`, `ForeignKeys`, or `ExclusionConstraints` stated alongside a non-empty `VirtualTableModule`.

`ColumnDef.Hidden` marks a column a SQLite virtual table module declares hidden: excluded from `SELECT *` and from an `INSERT` that names no column list, but still addressable by name. Its zero value, `false`, means an ordinary column, which is what every `ColumnDef` and every checked-in generated file written before this field existed has always meant. `TableDef.Validate` rejects a true `Hidden` on a table whose `VirtualTableModule` is empty, since a hidden column only ever comes from a virtual table module.

Both facts are describable but not yet renderable. `render.CreateTable` and the migrate diff-live path refuse to build DDL for a table naming `VirtualTableModule`, returning a `*render.UnsupportedVirtualTableError` that names the table and its module, since rasql does not yet know how to construct a `CREATE VIRTUAL TABLE` statement. There is no option-form constructor for either field yet: today they only appear on a descriptor `inspect` produced.

PostgreSQL and MySQL have no virtual-table concept, so neither field ever comes from a PostgreSQL or MySQL descriptor. `inspect.Table` also now describes a virtual table module's own backing table — what `PRAGMA table_list` calls a `"shadow"` table, such as `fts5`'s own `_data` table — the same way it describes an ordinary table, since a shadow table's own `CREATE TABLE` text is an entirely ordinary definition with no virtual-table facts of its own; it carries neither `VirtualTableModule` nor any `Hidden` column. Some virtual table modules, `fts5` among them, quote a shadow table's name with single quotes in its own `CREATE TABLE` text; rasql's SQLite parser accepts that form in table-name position, the one place SQLite itself falls back to reading a string literal as an identifier, so an `fts5` table's shadow tables describe the same as any other shadow table.

## Index methods

`IndexDef.Method` names a non-default index access method — what PostgreSQL calls an access method and MySQL calls an index type — such as PostgreSQL's `"gin"` or MySQL's `"FULLTEXT"`. Its zero value, the empty string, means the engine's own default: a plain B-tree, which is what every index built through `schema.Index`, `schema.UniqueIndex`, or a struct literal that leaves `Method` unset has always meant, so no existing descriptor or checked-in generated file changes meaning.

A non-default `Method` is describable but not yet renderable. `inspect.Table` records what a live PostgreSQL GIN index or MySQL FULLTEXT index actually uses instead of failing the whole table, as it used to, and `TableDef.Validate` accepts it. `render.CreateIndexes` and the migrate diff-live path still refuse to build DDL for one, returning a `*render.UnsupportedIndexMethodError` that names the index and its method, since rasql does not yet know how to construct anything other than a plain default index. There is no option-form constructor for a non-default `Method` yet: today it only appears on a descriptor `inspect` produced.

## Partial and expression indexes

`IndexDef.Predicate` and `IndexDef.Expressions` describe two more facts a real index can carry that `Columns` alone cannot: a `WHERE` condition, and keys that are not plain column references.

`Predicate` is the `WHERE`-clause expression text of a partial index, exactly as the server reports it. Its zero value, the empty string, means the index is not partial, which is what every `IndexDef` and every checked-in generated file written before this field existed has always meant.

`Expressions`, when non-empty, is the full ordered list of key expressions for an index that has at least one key that is not a plain column: an expression index, or one that mixes plain columns with expressions. A plain-column key inside it is recorded as its own bare column name, which is itself a valid SQL expression, so an index's full key order always lives in one place instead of being reconstructed by interleaving `Columns` with a second, position-keyed list. `Columns` is left empty whenever `Expressions` is set: an index either states its keys as `Columns`, the way every index has since before this field existed, or as `Expressions`, never a mix of the two, so `Columns` keeps meaning exactly what it has always meant — the ordered column names of a column-only index — rather than becoming an ambiguous subset of a mixed index's keys. `TableDef.Validate` rejects an `IndexDef` that sets both.

Both facts are describable but not yet renderable. `inspect.Table` records a live PostgreSQL or SQLite partial index's predicate, and a live PostgreSQL, MySQL, or SQLite expression index's key text, instead of failing the whole table, as it used to, and `TableDef.Validate` accepts both. `render.CreateIndexes` and the migrate diff-live path still refuse to build DDL for either: a non-empty `Predicate` returns a `*render.UnsupportedPartialIndexError`, and a non-empty `Expressions` returns a `*render.UnsupportedExpressionIndexError`, each naming the index and what it carries, since rasql does not yet know how to construct a `WHERE` clause or an expression key. MySQL has no partial-index concept, so `Predicate` never comes from a MySQL descriptor. Neither field has an option-form constructor yet: today they only appear on a descriptor `inspect` produced.

## Index key details

`IndexDef` carries three more facts a live index can attach beyond its key order, method, predicate, and expressions: `IncludeColumns`, `Invisible`, and `Keys`. `inspect.Table` now records all three instead of failing the whole table, as it used to whenever a PostgreSQL index named an `INCLUDE` clause, a MySQL index was marked `INVISIBLE`, or any key in a PostgreSQL, MySQL, or SQLite index was ordered `DESC`, named a non-default per-key collation or operator class, or (MySQL) indexed only a column prefix.

`IncludeColumns` lists a PostgreSQL index's `INCLUDE` columns, the index counterpart to `UniqueDef.IncludeColumns` (see [Unique constraint facts](#unique-constraint-facts)): columns carried by the index for covering reads without taking part in its key. Its zero value, `nil`, means the index has no `INCLUDE` clause, which is what every `IndexDef` written before this field existed has always meant.

`Invisible` marks a MySQL index the optimizer ignores for query planning without dropping it: MySQL still maintains it on every write and still enforces uniqueness through it. Its zero value, `false`, means the index is visible, which is what every `IndexDef` written before this field existed has always meant. PostgreSQL and SQLite have no index-visibility concept, so `Invisible` never comes from a PostgreSQL or SQLite descriptor.

`Keys`, when non-empty, is the full ordered list of per-key facts for an index that has at least one key ordered `DESC`, using a non-default collation or operator class, (MySQL) indexed over a column prefix, or (PostgreSQL) placing `NULL`s first or last against the default its own `ASC`/`DESC` direction implies. These five facts are positional, one per key, so none of them fits `Columns` or `Expressions`, both flat lists with no room to attach a second fact to one position. `Keys` replaces both as the sole source of the index's key order when set, the same way `Expressions` already replaces `Columns` for a mixed or all-expression index: `Columns` and `Expressions` are both left empty, and each `IndexKeyDef.Expression` carries that key's own text — a bare column name for a plain-column key, or expression text otherwise — exactly what `Expressions`'s elements already mean. `TableDef.Validate` rejects an `IndexDef` that sets `Keys` alongside `Columns` or `Expressions`. `IndexKeyDef`'s other fields, `Descending`, `Collation`, `OperatorClass`, `PrefixLength`, and `NullsOrder`, each keep their own zero value — `false`, `""`, `""`, `0`, and `""` — for the ordinary case: ascending, default collation, default operator class, whole column, default nulls placement. An index with no such key still describes its keys with `Columns` or `Expressions` alone, exactly as before `Keys` existed, so every descriptor and checked-in generated file written before this type existed stays valid.

`OperatorClass` and `NullsOrder` are PostgreSQL-only: MySQL and SQLite have no operator class or independent nulls-placement concept, so neither ever comes from a MySQL or SQLite descriptor. `PrefixLength` is MySQL-only: PostgreSQL and SQLite have no index-key-prefix concept, so it never comes from a PostgreSQL or SQLite descriptor. `Collation` comes from PostgreSQL or SQLite; MySQL has no per-key collation concept — its own `COLLATION` catalog column names sort order (`A`/`D`), which `Descending` already captures, not a collating sequence.

`NullsOrder` names a `schema.NullsOrder` — `schema.NullsFirst` or `schema.NullsLast` — recording a key's `NULLS FIRST`/`NULLS LAST` placement, but only when it overrides the placement its own `ASC`/`DESC` direction already implies: `NULLS LAST` for an ascending key, `NULLS FIRST` for a descending key. A key stated with the placement its direction already implies — an ascending key stated `NULLS LAST`, or a descending key stated `NULLS FIRST` — decodes to the same zero value as a key that states no placement at all, since the two are indistinguishable in their effect on sort order. Setting `NullsOrder` forces the index onto `Keys`, the same way `Descending`, `Collation`, `OperatorClass`, or `PrefixLength` does.

All facts on this page are describable but not yet renderable. `render.CreateIndexes` and the migrate diff-live path still refuse to build DDL for any of them: a non-empty `IncludeColumns` returns a `*render.UnsupportedIndexIncludeColumnsError`, a true `Invisible` returns a `*render.UnsupportedIndexInvisibleError`, and a non-empty `Keys` — including one whose only nondefault fact is `NullsOrder` — returns a `*render.UnsupportedIndexKeyDetailsError`, each naming the index and, where relevant, what it carries, since rasql does not yet know how to construct an `INCLUDE` clause, an `INVISIBLE` index, a `DESC` key, a non-default collation or operator class, a MySQL prefix part, or a `NULLS FIRST`/`NULLS LAST` override. None of these has an option-form constructor yet: today they only appear on a descriptor `inspect` produced.

## Index validity, storage, and placement facts

`IndexDef` carries five more facts a live PostgreSQL index can report about its own state and physical placement, beyond its key order, method, predicate, expressions, and per-key details: `NotValid`, `StorageParameters`, `Tablespace`, `ReplicaIdentity`, and `NullsNotDistinct`. `inspect.Table` now records all five instead of failing the whole table, as it used to whenever a PostgreSQL index was left invalid by a failed concurrent build, carried a storage parameter, lived outside the default tablespace, served as the table's replica identity, or, as a plain (non-constraint) unique index, was declared `NULLS NOT DISTINCT` — the last of the PostgreSQL index conditions that used to abort a sweep over a production schema outright.

`NotValid` records an index a failed `CREATE INDEX CONCURRENTLY` (or similar concurrent operation) left behind: PostgreSQL keeps the index's catalog entry but marks it unusable for lookups or constraint enforcement. Its zero value, `false`, means the index is valid and usable, which is what every `IndexDef` written before this field existed has always meant.

`StorageParameters` is a `map[string]string` of the index's storage parameters — settings such as `fillfactor` a `CREATE INDEX ... WITH (...)` clause attaches to an index at creation — keyed and valued exactly as the server reports them. Its zero value, `nil`, means the index carries no storage parameters, PostgreSQL's own default, which is what every `IndexDef` written before this field existed has always meant.

`Tablespace` names the tablespace holding the index, or the empty string for the database's default tablespace. Its zero value, `""`, means the default tablespace, which is what every `IndexDef` written before this field existed has always meant.

`ReplicaIdentity` marks the index PostgreSQL uses as the table's `REPLICA IDENTITY USING INDEX` for logical replication, in place of the primary key. Its zero value, `false`, means the index is not the replica identity, which is what every `IndexDef` written before this field existed has always meant. `TableDef.Validate` rejects a `ReplicaIdentity` index that is not also `Unique`, since PostgreSQL itself requires a replica identity index to be unique.

`NullsNotDistinct` marks a plain (non-constraint) unique index declared `NULLS NOT DISTINCT`, the same fact `UniqueDef.NullsNotDistinct` (see [Unique constraint facts](#unique-constraint-facts)) records for a named unique constraint. Its zero value, `false`, means `NULLS DISTINCT`, the SQL default and the only behavior every `IndexDef` written before this field existed has always meant. `TableDef.Validate` rejects a `NullsNotDistinct` index that is not also `Unique`, since `NULLS NOT DISTINCT` only applies to a unique index.

All five facts are PostgreSQL-only: MySQL and SQLite have no equivalent to any of them, so none ever comes from a MySQL or SQLite descriptor. All five are describable but not yet renderable. `render.CreateIndexes` and the migrate diff-live path still refuse to build DDL for any of them: `NotValid` returns a `*render.UnsupportedIndexNotValidError`, a non-empty `StorageParameters` returns a `*render.UnsupportedIndexStorageParametersError`, a non-empty `Tablespace` returns a `*render.UnsupportedIndexTablespaceError`, `ReplicaIdentity` returns a `*render.UnsupportedIndexReplicaIdentityError`, and `NullsNotDistinct` returns a `*render.UnsupportedIndexNullsNotDistinctError`, each naming the index and, where relevant, what it carries, since rasql does not yet know how to construct an invalid index, a `WITH (...)` storage clause, a `TABLESPACE` clause, a `REPLICA IDENTITY USING INDEX` declaration, or a `NULLS NOT DISTINCT` clause. None of the five has an option-form constructor yet: today they only appear on a descriptor `inspect` produced.

No PostgreSQL condition on a plain index or its keys causes inspection to reject a table anymore. A key's `NULLS FIRST`/`NULLS LAST` placement that overrides its own `ASC`/`DESC` default is recorded on `IndexKeyDef.NullsOrder` instead (see [Index key details](#index-key-details)), and a named `UNIQUE` constraint whose own backing index carries a non-default collation, storage parameters, tablespace, or replica identity is recorded on `UniqueDef` (see [Unique constraint facts](#unique-constraint-facts)).

A partial index's `Predicate` and an expression key's text are each the server's own re-serialized form, not necessarily the original DDL text: PostgreSQL deparses a partial index's `WHERE` clause and an expression key from the index's stored internal form rather than keeping the original characters around. An index built `WHERE celsius * 9 / 5 + 32 > 100` inspects with `Predicate` equal to `(celsius * 9 / 5 + 32) > 100` — a wrapping pair of parentheses around the arithmetic that the original source never had, added because PostgreSQL's deparser parenthesizes that operand of `>`. This is a narrower normalization than `ColumnDef.GeneratedExpression`'s (see [Generated columns](#generated-columns)): PostgreSQL's index predicate and expression deparse route only adds parentheses where its own deparser judges them needed, not around every single operator the way `GeneratedExpression` is, so an index built `((celsius * 9 / 5 + 32))` inspects with `Expressions` holding `(celsius * 9 / 5 + 32)`, with no parentheses inserted around `celsius * 9 / 5` itself. Either way, `Predicate` and `Expressions` are not guaranteed to match a hand-written migration's exact spacing or parenthesization. SQLite is the exception, the same way it is for `GeneratedExpression`: its `Predicate` and `Expressions` come from parsing the index's own checked-in `CREATE INDEX` text, so they preserve whatever the source actually wrote.

## Foreign key facts

`ForeignKeyDef` carries seven facts a live database can attach to a foreign key beyond a plain `REFERENCES` clause: `ReferencedSchema` (documented under [Qualify a table with a schema](#qualify-a-table-with-a-schema)), `Match`, `Deferrable`, `NotValid`, `NotEnforced`, `Temporal`, and `DeleteSetColumns`. `inspect.Table` now records all seven instead of failing the whole table, as it used to whenever a PostgreSQL or SQLite foreign key used any of them, or a PostgreSQL foreign key was declared `PERIOD` or stated a column list on `ON DELETE SET NULL`/`SET DEFAULT` — the last two of the PostgreSQL foreign key conditions that used to abort a sweep over a production schema outright — and `TableDef.Validate` accepts them.

`MatchType` names a foreign key's `MATCH` clause. Its zero value, the empty string, means `MATCH SIMPLE`, the SQL default and the only form every descriptor written before this field existed has always meant. `schema.MatchFull` and `schema.MatchPartial` name the other two forms SQL defines.

`Deferrability` names when the constraint is checked. Its zero value, the empty string, means `NOT DEFERRABLE`, again the only form every prior descriptor has meant. `schema.DeferrableInitiallyImmediate` and `schema.DeferrableInitiallyDeferred` name a deferrable key that checks at the end of each statement by default, or at transaction commit by default, respectively.

`NotValid` and `NotEnforced` are plain `bool` fields rather than named types, because each names a single yes/no fact rather than a choice among several forms the way `Match` and `Deferrable` do: PostgreSQL's `NOT VALID` and `NOT ENFORCED` clauses are either stated or not, with nothing else to name. Both are spelled in the negative, mirroring the SQL keywords themselves, so that the zero value, `false`, reads as "not NOT VALID" — an ordinary validated, enforced foreign key — which is what every descriptor written before these fields existed has always meant; a positively named `Validated`/`Enforced` pair would need `true` as its ordinary default, the opposite of every other new field's zero value in this package. `NotValid` records that existing rows were never checked against the foreign key. `NotEnforced` records that PostgreSQL 18+ has stopped enforcing it at all; MySQL foreign keys have no `NOT VALID` or `NOT ENFORCED` concept, so both fields never come from a MySQL descriptor.

`Temporal` marks a PostgreSQL 18+ temporal foreign key, declared with `PERIOD` on both the referencing and referenced columns. Its zero value, `false`, means the foreign key is an ordinary, non-temporal `FOREIGN KEY`, which is what every `ForeignKeyDef` written before this field existed has always meant. MySQL has no temporal-foreign-key concept, so `Temporal` never comes from a MySQL descriptor.

`DeleteSetColumns` lists the subset of `Columns` a PostgreSQL 16+ `ON DELETE SET NULL (columns)` or `ON DELETE SET DEFAULT (columns)` clause names, in the order the server reports them. Its zero value, `nil`, means the `SET NULL`/`SET DEFAULT` action, if stated in `OnDelete`, applies to every column in `Columns`, the SQL default and what every `ForeignKeyDef` written before this field existed has always meant; `DeleteSetColumns` is meaningless, and always `nil`, unless `OnDelete` is `schema.SetNull` or `schema.SetDefault`, which `TableDef.Validate` enforces. MySQL has no column-list concept for these actions, so `DeleteSetColumns` never comes from a MySQL descriptor.

All seven facts are describable but not yet renderable. `render.CreateTable` and the migrate diff-live path refuse to build DDL for a foreign key naming a non-default `Match` or `Deferrable`, or naming `NotValid`, `NotEnforced`, `Temporal`, or `DeleteSetColumns`, returning a `*render.UnsupportedForeignKeyMatchError`, `*render.UnsupportedForeignKeyDeferrabilityError`, `*render.UnsupportedForeignKeyNotValidError`, `*render.UnsupportedForeignKeyNotEnforcedError`, `*render.UnsupportedForeignKeyTemporalError`, or `*render.UnsupportedForeignKeyDeleteSetColumnsError` that names the foreign key and, where relevant, the fact it named, since rasql does not yet know how to construct anything other than a plain `MATCH SIMPLE`, `NOT DEFERRABLE`, validated, enforced, non-temporal foreign key with a plain `ON DELETE SET NULL`/`SET DEFAULT` applying to every referencing column. `ReferencedSchema` is not part of that restriction: both PostgreSQL and MySQL already render it, and SQLite already refuses a genuinely cross-schema one, so a foreign key that only names a different schema renders normally. There is no option-form constructor for a non-default `Match`, `Deferrable`, `NotValid`, `NotEnforced`, `Temporal`, or `DeleteSetColumns` yet: today they only appear on a descriptor `inspect` produced.

SQLite's own introspection cannot report `Match` or `Deferrable` through a PRAGMA the way it reports `ON DELETE`/`ON UPDATE` actions: `PRAGMA foreign_key_list` always reports a foreign key's match as `"NONE"` regardless of what its `REFERENCES` clause actually declared, and reports no deferrability at all. `inspect` reads both from the table's own `CREATE TABLE` text instead. SQLite has no `NOT VALID` or `NOT ENFORCED` concept for foreign keys either, so `NotValid` and `NotEnforced` never come from a SQLite descriptor.

## Check constraint facts

`CheckDef` carries three facts a live database can attach to a check constraint beyond its expression: `NoInherit`, `NotValid`, and `NotEnforced`. `inspect.Table` now records all three instead of failing the whole table, as it used to whenever a PostgreSQL check constraint used any of them, and `TableDef.Validate` accepts them. Each is a plain `bool` for the same reason `ForeignKeyDef.NotValid` and `.NotEnforced` are: a yes/no fact rather than a choice among forms, spelled in the negative after its SQL keyword so the zero value, `false`, means the ordinary case — inherited, validated, enforced — that every descriptor written before these fields existed has always meant.

`NoInherit` records a check constraint declared `NO INHERIT`: a child table in a PostgreSQL inheritance hierarchy neither inherits nor enforces it. `NotValid` records a check constraint declared `NOT VALID`: existing rows were never checked against it. `NotEnforced` records a check constraint declared `NOT ENFORCED`, which both PostgreSQL 18+ and MySQL support; MySQL's `information_schema.table_constraints.enforced` reports this fact directly, which `inspect` now records instead of rejecting, the same way it now handles the PostgreSQL case. MySQL has no `NO INHERIT` or `NOT VALID` concept for check constraints, and SQLite has none of the three, so `NoInherit` and `NotValid` never come from a MySQL descriptor, and none of the three ever comes from a SQLite descriptor.

All three facts are describable but not yet renderable. `render.CreateTable` refuses to build DDL for a check constraint naming any of them, returning a `*render.UnsupportedCheckNoInheritError`, `*render.UnsupportedCheckNotValidError`, or `*render.UnsupportedCheckNotEnforcedError` that names the check constraint, since rasql does not yet know how to construct a `NO INHERIT`, `NOT VALID`, or `NOT ENFORCED` check constraint; the migrate diff-live path inherits the same refusal because it builds its SQL through `render.CreateTable`. There is no option-form constructor for any of them yet: today they only appear on a descriptor `inspect` produced.

## Exclusion constraints

`TableDef.ExclusionConstraints` lists a table's PostgreSQL `EXCLUDE` constraints: a generalization of `UniqueConstraints` that forbids any two rows from having elements that all satisfy their paired operator against each other, rather than always meaning equality. `inspect.Table` now records a live PostgreSQL exclusion constraint as a `schema.ExclusionDef` instead of failing the whole table, as it used to whenever a table carried one. Its zero value, `nil`, means the table has no exclusion constraints, which is what every `TableDef` and every checked-in generated file written before this field existed has always meant. Exclusion constraints are PostgreSQL-only; MySQL and SQLite have no equivalent concept, so `ExclusionConstraints` never comes from a MySQL or SQLite descriptor.

`ExclusionDef` mixes structured facts with server-reported text, the same split `IndexDef` makes between `Method`/`Predicate` and `Expressions`. `Name` and `Method` are structured: `Method` names the constraint's backing index access method, such as `"gist"`, using the same `schema.IndexMethod` type and the same zero-value convention as `IndexDef.Method` — the empty string means the engine's own default access method (`btree`). `Elements` lists the constraint's elements in declaration order as `schema.ExclusionElementDef` values, each pairing an `Expression` (a bare column name for a plain column element, or the element's expression text otherwise, exactly as the server reports it, the same convention `IndexDef.Expressions` uses) with the `Operator` checked against it, exactly as the server reports it, such as `"="` or `"&&"`. `Predicate` is the `WHERE`-clause expression text of a partial exclusion constraint, on the same convention as `IndexDef.Predicate`. `Deferrable` reuses `schema.Deferrability`, the same type `ForeignKeyDef.Deferrable` and `UniqueDef.Deferrable` use, rather than a second type spelling the same three states.

An `ExclusionDef` is describable but not yet renderable: `TableDef.Validate` accepts it, but `render.CreateTable` and the migrate diff-live path refuse to build DDL for one, returning a `*render.UnsupportedExclusionConstraintError` that names the constraint, since rasql does not yet know how to construct an `EXCLUDE` clause. There is no option-form constructor yet: today an `ExclusionDef` only appears on a descriptor `inspect` produced.

## Generated columns

`ColumnDef.GeneratedExpression` and `ColumnDef.GeneratedStorage` describe a generated column: one whose value the database computes from an expression over other columns, rather than one an `INSERT` or `UPDATE` can write to directly. `inspect.Table` records both on all three engines instead of failing the whole table, or, on PostgreSQL, instead of silently describing the column as an ordinary writable one. A SQLite column that is genuinely hidden, the kind a virtual table module declares, is a different fact, `ColumnDef.Hidden`: see [SQLite virtual tables](#sqlite-virtual-tables).

`GeneratedExpression` is the expression text exactly as the server reports it, or empty for an ordinary column — its zero value, which is what every `ColumnDef` and every checked-in generated file written before this field existed has always meant. `GeneratedStorage` is a `schema.GeneratedStorage` naming whether the expression is written into the table (`schema.GeneratedStored`) or computed each time the column is read (`schema.GeneratedVirtual`); it is meaningless unless `GeneratedExpression` is also stated, and `TableDef.Validate` rejects one stated without the other, or a generated column that also states `Default`, since a generated column's value comes from its expression and SQL itself does not let the two coexist.

Both facts are describable but not yet renderable. `render.CreateTable` refuses to build DDL for a generated column, returning a `*render.UnsupportedGeneratedColumnError` that names the column, its expression, and its storage kind, since rasql does not yet know how to construct a `GENERATED ALWAYS AS` clause, and because silently rendering a generated column as an ordinary writable one would be a worse failure than most this package refuses: a generated column cannot be written to at all. The migrate diff-live path inherits the same refusal because it builds its SQL through `render.CreateTable`. There is no option-form constructor yet: today the two fields only appear on a descriptor `inspect` produced.

All three engines are covered. SQLite reports a generated column through `PRAGMA table_xinfo`'s hidden flag, with the expression text parsed back out of the table's own `CREATE TABLE` definition. MySQL reports one through `information_schema.columns.EXTRA`, spelled exactly `STORED GENERATED` or `VIRTUAL GENERATED`, alongside `GENERATION_EXPRESSION`. PostgreSQL reports one through `information_schema.columns.is_generated` and `.generation_expression`; PostgreSQL has no storage-kind column in `information_schema`, so `inspect.Table` reads that from `pg_catalog.pg_attribute.attgenerated` instead (`s` for stored, or, from PostgreSQL 18, `v` for virtual — PostgreSQL versions before 18 support only stored generated columns). Before this, PostgreSQL inspection selected no generated-column metadata at all, so a generated column inspected as an ordinary column with an empty default, silently losing the fact that it could not be written to. That gap, not a rejection, is what made a PostgreSQL generated column worth fixing here.

On PostgreSQL and MySQL, `GeneratedExpression` is the server's own re-serialized, normalized form of the expression, not the source text a migration wrote — both engines parse the `GENERATED ALWAYS AS` clause once at `CREATE TABLE` time and report it back from that parsed form, fully parenthesized, rather than keeping the original characters around. A column declared `GENERATED ALWAYS AS (celsius * 9 / 5 + 32) STORED` inspects with `GeneratedExpression` equal to `(((celsius * 9) / 5) + 32)` on PostgreSQL, or `` (((`celsius` * 9) / 5) + 32) `` on MySQL, never the `celsius * 9 / 5 + 32` a person typed. A descriptor `rasqlgen` generates from a live PostgreSQL or MySQL database therefore will not textually match a hand-written migration's expression even when they mean the same thing, and regenerating from that database reproduces the normalized form again, not the original. SQLite is the exception: its `GeneratedExpression` comes from parsing the table's own checked-in `CREATE TABLE` text (see `sqliteGeneratedExpression` in `inspect/inspect.go`), so it preserves whatever the source actually wrote, spacing included.

A generated column changes nothing about code generation: `rasqlgen` still emits an ordinary row field for it, since a generated column reads back like any other column, and the field's Go type follows the same rules as any other column of its logical type. It is only the write path that treats it differently, and automatically: `rasql.Insert`, `rasql.InsertMany`, `rasql.Update`, and `rasql.UpdateMany` all leave a `GeneratedExpression` column out of the column list they build by default, the same way `rasql.UpdateWithOptions` already leaves the primary key out of a plain `Update`'s assignment list, because a database rejects a statement that targets a generated column explicitly. A caller does not need `rasql.DefaultColumns` or `rasql.UpdateColumns` to get this: those options still work for their existing purpose (a database-default or auto-increment column an ordinary, non-generated column happens to have), but naming a generated column through `rasql.UpdateColumns` is refused up front rather than silently accepted or left to fail against the database.

## Relationships

`ForeignKeys` remain the source of database constraints. `rasqlgen` derives a `schema.RelationshipDef` with kind `schema.RelationshipBelongsTo` for each foreign key that has no matching entry in `Relationships`; the `schema.RelationshipNamed` foreign-key option states one explicitly, in the option form, instead. Set `Relationships` explicitly when the generated method name should differ from the local column name, but keep its local columns and referenced schema, table, and columns matched to a declared foreign key. Relationship metadata does not change DDL.

The generated API covers one bounded slice: a non-null single-column foreign key that targets a non-null single-column primary key with the same generated Go type. When both tables are generated in the package, the child table exposes a belongs-to method and the parent table exposes the inverse has-many method. Each relation exposes `Join` and `Load`; `Load` fetches all related rows with one secondary `IN` query and groups them by key. Callers must split very large parent slices themselves when they approach the database parameter limit.

Composite keys, nullable foreign keys, nullable or non-primary target columns, many-to-many links, polymorphic links, nested preloading, and relationships whose target table is not generated in the package remain unsupported. The foreign key and its ordinary SQL join remain available for each of those cases.

## Name the generated row type

`rasqlgen` names the Go row type it generates for a table `<Table>Row`: a table named `users` generates `UsersRow`. `schema.RowNamed` (or `TableDef.RowName` on a struct literal) overrides that default, so a table can generate `User` instead and let calling code read `store.User` rather than `store.UsersRow` — the row type is the one generated name a caller writes throughout their own code.

Nothing is guessed: `rasqlgen` never singularizes a table name to derive a row name on its own. Stripping a trailing `s` produces `Addresse` from `addresses`, `Serie` from `series`, and `Bu` from `bus`, and the bare table name does not compile as a row type either way, since `type Users` would collide with the generated `Users()` accessor. `RowName` is a code-generation hint only: no renderer, dialect, `inspect`, or `migrate` path reads it, and it never appears in rendered SQL.

## Qualify a table with a schema

`Schema` is optional and names the namespace holding the table: a PostgreSQL schema, a MySQL database, or a SQLite attached-database name. rasql takes no position on what a namespace means to a server: it validates `Schema` as a simple identifier exactly like `Name`, quotes it as a separate identifier in the SQL that reads the field, and never creates, drops, or connects to a namespace itself. An application that needs `audit.events` to exist creates it with a reviewed native migration, the same way every other piece of DDL this library does not synthesize gets created. An empty `Schema` leaves the table unqualified, which resolves through the connection's own default and is what every descriptor written before this field existed still does.

Qualification reaches DML, column references, and DDL. A `SELECT`, `INSERT`, `UPDATE`, or `DELETE` built from a qualified descriptor renders `"audit"."events"` as its target, a column reached through the unaliased table renders `"audit"."events"."id"`, and `render.CreateTable`, `render.CreateIndexes`, and `rasql.CreateTable` render `CREATE TABLE "audit"."events"` and its indexes into the named namespace on every dialect that can express it. rasql never creates, drops, or connects to the namespace itself: an application that needs `audit` to exist creates it with a reviewed native migration, the same way every other piece of DDL this library does not synthesize gets created, and `rasql.CreateTable` then fails with the server's own error if that namespace does not exist. SQLite inspection preserves the database name in `Schema`, including when a lookup is scoped with `TableIn`, and [`rasqlgen`](06-rasqlgen.md) emits that non-empty `Schema` value in generated descriptors. PostgreSQL and MySQL inspection leave `Schema` empty, so `rasqlgen` emits no `Schema` field for those dialects. Qualified PostgreSQL and MySQL inspection and generation are not supported yet, so a qualified table on those dialects is re-read through a hand-written descriptor.

A foreign key that references a table in another schema names it with `ForeignKeyDef.ReferencedSchema`, validated the same way as `Table.Schema` and left empty for the server to resolve, exactly like an empty `Table.Schema`. PostgreSQL and MySQL render a stated `ReferencedSchema` as a second qualified identifier in the `REFERENCES` clause. SQLite cannot: it rejects a schema-qualified `REFERENCES` outright, even when the reference names the referencing table's own schema, so rasql drops a same-schema qualifier there rather than refuse a reference that means the same thing either way, and refuses to render a genuinely cross-schema reference instead of silently pointing it at the wrong table. An unqualified table's foreign keys are unaffected either way: qualifying `Table.Schema` alone, without also stating `ForeignKeyDef.ReferencedSchema`, would let PostgreSQL resolve an unqualified `REFERENCES` through the connection's `search_path` rather than the table's own schema, which is why the two fields ship together. `inspect.Table` fills `ReferencedSchema` for a PostgreSQL or MySQL foreign key that references a table outside the current schema instead of failing the whole table, as it used to.

`schema.TableDef` and `query.TableRef` each answer two questions about qualification. `Qualified` reports whether a schema is named at all, and `QualifiedName` returns `schema.name` for display, falling back to `name` for an unqualified table. Neither is a SQL identifier: a renderer quotes `Schema` and `Name` as two identifiers, and `dialect.QuoteIdentifier` rejects the dotted string `QualifiedName` returns. On `query.TableRef` the two describe the table rather than the reference: `Qualified` stays true once the table is aliased, while `QualifiedName` returns the alias, because that is what an error message about an aliased table has to name. `query.TableRef.QualifierSchema` reports what actually qualifies a rendered column, which is nothing at all once an alias replaces the table's whole name.

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
	// namespace in production; rasql.CreateTable then renders CREATE TABLE
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

	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}

	// InSchema qualifies the table without changing how any other option works.
	events := rasql.MustTableOf[eventRow](schema.MustTableDef("events",
		schema.InSchema("audit"),
		schema.Integer("id"),
		schema.Text("action"),
		schema.PrimaryKey("id"),
	))

	// SQL: CREATE TABLE audit.events (id INTEGER NOT NULL, action TEXT NOT NULL, PRIMARY KEY (id))
	if err := rasql.CreateTable(ctx, db, events); err != nil {
		fmt.Printf("failed to create events table: %s\n", err)
		return
	}

	// SQL: INSERT INTO audit.events (id, action) VALUES (?, ?) (arguments: 1, "created")
	if _, err := rasql.Insert(ctx, db, events, eventRow{ID: 1, Action: "created"}); err != nil {
		fmt.Printf("failed to insert event: %s\n", err)
		return
	}

	eventID, err := events.Column("id")
	if err != nil {
		fmt.Printf("failed to reference id column: %s\n", err)
		return
	}
	// SQL: SELECT audit.events.id, audit.events.action FROM audit.events WHERE audit.events.id = ? (argument: 1)
	event, err := rasql.SelectFrom(events).WhereEqual(eventID, int64(1)).One(ctx, db)
	if err != nil {
		fmt.Printf("failed to query events: %s\n", err)
		return
	}

	// QualifiedName is for display only, never a SQL identifier: the renderer
	// quotes Schema and Name as two separate identifiers.
	fmt.Printf("%s: %s\n", events.Ref().QualifiedName(), event.Action)

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

A column also carries `Nullable`, `Default`, and its concrete `Type`. Type-specific options live on that type: `IntegerType.Unsigned` describes unsigned integers, `IntegerType.DisplayWidth` and `.ZeroFill` describe two MySQL-only integer modifiers (see [Integer display width and ZEROFILL](#integer-display-width-and-zerofill)), `TextType.Width` describes a text column's maximum number of characters, and `DecimalType` carries `Precision` and `Scale`. `ColumnDef.GeneratedExpression` and `.GeneratedStorage` describe a generated column (see [Generated columns](#generated-columns)). Identifiers must be simple: `schema.ValidateIdentifier` accepts a leading letter or underscore followed by letters, digits, or underscores, and everything else is rejected rather than quoted around.

`schema.DecimalType` is an exact decimal, for money, quantities, and any other value a binary floating-point `FloatType` would round. A decimal type must set `Precision` (the total number of significant digits, at least 1) and `Scale` (the number of those digits right of the decimal point, no more than `Precision`); `TableDef.Validate` rejects a decimal type that omits either. `Scale` is a `schema.DecimalScale` rather than a plain `int`, and is stated with `schema.NewDecimalScale`, because a `DECIMAL(19,0)` column is legitimate and its zero scale has to be distinguishable from a descriptor that named no scale at all; the zero value of `schema.DecimalScale` means "no scale stated" and `DecimalScale.Value` returns the stated scale together with whether one was stated. Each dialect renders `Precision`/`Scale` into its own DDL: PostgreSQL and MySQL render `NUMERIC(p,s)` and `DECIMAL(p,s)`, each exact and each enforcing its own maximum precision and scale. On both, a decimal column decodes to its declared scale in string form, zero-padded on the right: a `NUMERIC(19,4)` column yields `"19.9900"` for the value `19.99`, not `"19.99"`, so a caller comparing decimal strings has to compare on the declared scale. That declared scale governs the column itself; a projected expression over it need not keep it, and [Scalar functions](03-querying.md#scalar-functions) states where MySQL widens one. SQLite has no exact decimal storage class, so it renders `TEXT` instead: the column round-trips its digits exactly and applies no such padding, decoding to a Go `string` on every dialect, but a SQLite decimal column compares and orders lexicographically rather than numerically, since it is stored as text rather than a number. A caller that wants a real decimal type in Go, rather than a `string`, can write its own row struct with a field implementing `sql.Scanner` and `driver.Valuer`; `rasql.ScanValue` checks for that interface before every built-in conversion, so the raw driver value reaches it unchanged.

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

	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}

	// schema.Decimal takes precision and scale positionally, rather than as
	// options, because TableDef.Validate rejects a decimal column that lacks
	// either: stating both here makes an incomplete decimal column impossible
	// to construct in the first place instead of merely rejected once
	// assembled.
	invoices := rasql.MustTableOf[invoiceRow](schema.MustTableDef("invoices",
		schema.Integer("id"),
		schema.Decimal("amount", 19, 4),
		schema.PrimaryKey("id"),
	))
	// SQLite has no exact decimal storage class, so the dialect declares this
	// column TEXT rather than NUMERIC(19,4), which would round through REAL.
	// SQL: CREATE TABLE invoices (id INTEGER NOT NULL, amount TEXT NOT NULL, PRIMARY KEY (id))
	if err := rasql.CreateTable(ctx, db, invoices); err != nil {
		fmt.Printf("failed to create invoices table: %s\n", err)
		return
	}

	// SQL: INSERT INTO invoices (id, amount) VALUES (?, ?) (arguments: 1, "19.99")
	if _, err := rasql.Insert(ctx, db, invoices, invoiceRow{ID: 1, Amount: "19.99"}); err != nil {
		fmt.Printf("failed to insert invoice: %s\n", err)
		return
	}

	invoiceID, err := invoices.Column("id")
	if err != nil {
		fmt.Printf("failed to reference id column: %s\n", err)
		return
	}
	// SQL: SELECT invoices.id, invoices.amount FROM invoices WHERE invoices.id = ? (argument: 1)
	invoice, err := rasql.SelectFrom(invoices).WhereEqual(invoiceID, int64(1)).One(ctx, db)
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

## Integer display width and ZEROFILL

`IntegerType.DisplayWidth` and `IntegerType.ZeroFill` record two more MySQL-only facts about an integer column: a stated display width, such as the `11` in `int(11)`, and the `ZEROFILL` attribute, which left-pads a displayed value with zeros up to its display width and implies `UNSIGNED`. Neither is a constraint on the values the column stores; a display width is purely a minimum number of digits MySQL pads in query output, the same way `int(3)` still stores `12345` in full. `inspect.Table` now records both instead of failing the whole table, as it used to whenever a MySQL integer column carried either.

`DisplayWidth` is a `schema.IntegerDisplayWidth`, stated with `schema.NewIntegerDisplayWidth`, following the same "stated vs. unstated" convention as `TextType.Width` and `DecimalType.Scale`: its zero value means no width was stated at all, distinct from a stated width of `0`, and `IntegerDisplayWidth.Value` returns the stated width together with whether one was stated. `ZeroFill` is a plain `bool`; its zero value, `false`, means the column carries no `ZEROFILL`. Both zero values are what every `IntegerType` and every checked-in generated file written before these fields existed has always meant.

Both facts are describable but not yet renderable. `render.CreateTable` refuses to build DDL for an integer column stating either, returning a `*render.UnsupportedIntegerDisplayWidthError` or `*render.UnsupportedIntegerZeroFillError` that names the column and the fact it named, since rasql does not yet know how to construct an `INT(n)` or `ZEROFILL` declaration; the migrate diff-live path inherits the same refusal because it builds its SQL through `render.CreateTable`. There is no option-form constructor for either yet: today they only appear on a descriptor `inspect` produced.

MySQL 8.0.19 and later deprecate the display width for an ordinary integer column and may no longer report one back, though a column carrying `ZEROFILL` still states and reports its width. PostgreSQL and SQLite have neither concept, so neither field ever comes from a PostgreSQL or SQLite descriptor.

Only `BIGINT UNSIGNED` actually gains range from this. Every narrower unsigned type — `TINYINT UNSIGNED` through `INT UNSIGNED` — fits inside a signed `BIGINT` already, so a column of one of those loses no representable value either way; what it gains is that the descriptor now says what the column is, and re-rendering it keeps the `UNSIGNED` the database had.

[`rasqlgen`](06-rasqlgen.md) generates a `uint64` field for an unsigned column instead of an `int64` one, because `int64` cannot hold the top half of the range. `rasql.ScanValue` fills either field from an integer driver value of either signedness and reports an error, rather than wrapping, for a value the field cannot hold: which signedness a driver delivers is the driver's choice rather than the column's.

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
	events := schema.MustTableDef("events",
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

## Text column width

A `schema.TextType` column states no maximum length unless `schema.Width(n)` says otherwise; other concrete column types cannot carry this option. An unstated width, the zero value of `schema.TextType.Width`, is not the same as a stated width of 0: `schema.TextWidth.Value` returns the stated width together with whether one was stated at all, the same distinction `schema.DecimalScale` makes for a decimal's scale.

Width exists because MySQL refuses to build a key over an unbounded `TEXT` column: creating an index, primary key, or unique constraint over one fails with its own error 1170, "BLOB/TEXT column used in key specification without a key length". `render.CreateTable` and `render.CreateIndexes` check for this ahead of MySQL and refuse to render such a statement, naming the column and pointing at `schema.Width`, rather than send MySQL a statement it is going to reject anyway. PostgreSQL and SQLite index, and build a primary key or unique constraint over, an unbounded text column natively, so they are not checked.

Each dialect renders a stated width differently. MySQL and PostgreSQL both render `VARCHAR(n)`, which they enforce on different terms. PostgreSQL rejects an insert past the limit with SQLSTATE `22001`, truncating instead only when the excess characters are all spaces. MySQL rejects it with error 1406 only under strict SQL mode — `STRICT_TRANS_TABLES` or `STRICT_ALL_TABLES`, on by default since MySQL 5.7 — and truncates the value with a warning without it. That rendered width is also what satisfies MySQL's key-length requirement, so stating a width is enough to make the column indexable. SQLite renders plain `TEXT` regardless of a stated width: it assigns column storage by affinity rather than by declared type, so a `VARCHAR(n)` column there would be stored and enforced exactly like `TEXT`, and rendering `VARCHAR(n)` syntax would claim an enforcement that never happens. An unstated width always renders each dialect's plain, unbounded text type.

A stated width also says nothing about whether the column is fixed-width. `schema.Fixed()`, combined with `schema.Width(n)`, marks it so: MySQL and PostgreSQL then render `CHAR(n)` instead of `VARCHAR(n)`. `Fixed` without a stated width is rejected — bare `CHAR` means `CHAR(1)`, not an unbounded column — so `Table.Validate` refuses that combination regardless of which option ran first. SQLite renders plain `TEXT` for a fixed-width column too, for the same reason it ignores a stated width at all: its type affinity never enforces either.

Inspecting a live MySQL or PostgreSQL database preserves the width a `CHAR(n)`/`VARCHAR(n)` or `CHARACTER(n)`/`CHARACTER VARYING(n)` column states, so a column created with `schema.Width` round-trips through `inspect.Inspector` unchanged. MySQL's `TEXT`, `ENUM`, and `SET` columns normalize to an unstated width, since none of them carries a plain numeric length in MySQL's catalog, and so does PostgreSQL's `TEXT` and an unbounded `CHARACTER VARYING`. Inspection also records fixed-ness: MySQL's `CHAR` and PostgreSQL's `CHARACTER` both normalize with `schema.TextType.Fixed` set, and re-render as `CHAR(n)`, so a `CHAR(n)`/`CHARACTER(n)` column round-trips through `inspect.Inspector` and back through `render.CreateTable` without `migrate/diff` reporting a phantom change.

## Bind a row type to the table

A bare `schema.TableDef` describes the database. Pairing it with a Go type produces a `rasql.Table[T]`, which is what the typed API takes:

<!-- INCLUDE(examples/schema_bind_row_type_example_test.go#bind_row_type) -->
```go
type UserRow struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

users := rasql.MustTableOf[UserRow](definition)
```
source: [examples/schema_bind_row_type_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/schema_bind_row_type_example_test.go)
<!-- END INCLUDE -->

Each field's `rasql` tag names the column it holds. `rasql.MustTableOf` panics on an invalid descriptor and suits generated or otherwise constant tables; `rasql.TableOf` returns the error instead, for descriptors assembled at runtime.

A `rasql.Table[T]` is half of a table value rather than the whole of it. Wrap it in a type with one accessor method per column, calling `rasql.ColumnOf`, so that `users.ID()` is the column reference the builders take. That is the shape [`rasqlgen`](06-rasqlgen.md) emits, the shape every example on these pages uses, and the shape a hand-written table should have too. [Getting started](01-getting-started.md#the-table-used-throughout-the-documentation) shows the full wrapper for the `users` table, and [What the column accessors catch](06-rasqlgen.md#what-the-column-accessors-catch) shows what the accessors are worth.

Two methods remain for code that only learns a column name while it runs. `users.Column(name)` looks a column up and returns a `query.ColumnRef` with an error, and `users.Ref()` returns the underlying `query.TableRef` that the lower-level `query` package works in terms of, which [Querying](03-querying.md) uses for joins and projections.

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

`Inspector.TableNames(ctx)` returns the base tables in the inspected scope as `[]inspect.TableName`, excluding views and sorted by `Schema` then `Name`, so a caller does not need to already know a table name to start inspecting it. PostgreSQL scopes to `current_schema()` and MySQL to `DATABASE()`, the same scope `Table` reads columns from, and both leave every `TableName.Schema` empty: `Table` itself never fills `schema.TableDef.Schema` for those two dialects, and filling it here would silently qualify SQL that is unqualified today. SQLite has no single equivalent scope: like `Table`'s own default, `TableNames` reports across `main`, `temp`, and every database attached to the connection, with `TableName.Schema` naming which database each table came from — the field a bare table name cannot carry, and why two databases holding a table of the same name still come back as two distinguishable results. `Inspector.TableNamesIn(ctx, databaseName)` scopes SQLite to one database instead, the enumeration counterpart of `TableIn`, and carries the same retained-connection requirement for `temp` or an attached database; every `TableName.Schema` it returns equals `databaseName`. `TableNamesIn` is supported only for SQLite.

<!-- INCLUDE(examples/inspect_sqlite_table_names_example_test.go) -->
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

func Example_inspect_sqlite_table_names() {
	// This example enumerates the base tables across main and an attached
	// database, including a table name that exists in both.
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
	if _, err := connection.ExecContext(ctx, "ATTACH DATABASE ':memory:' AS tenant"); err != nil {
		fmt.Printf("failed to attach tenant database: %s\n", err)
		return
	}
	for _, statement := range []string{
		"CREATE TABLE main.armadillos (id INTEGER PRIMARY KEY)",
		"CREATE TABLE main.zebras (id INTEGER PRIMARY KEY)",
		"CREATE TABLE tenant.zebras (id INTEGER PRIMARY KEY)",
		// A view is not a base table, so TableNames excludes it.
		"CREATE VIEW main.zebra_view AS SELECT id FROM main.zebras",
	} {
		if _, err := connection.ExecContext(ctx, statement); err != nil {
			fmt.Printf("failed to run %q: %s\n", statement, err)
			return
		}
	}

	inspector, err := inspect.New(connection, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create SQLite inspector: %s\n", err)
		return
	}
	// TableNames reports every database's tables together; TableName.Schema
	// is what keeps the two "zebras" tables distinguishable.
	refs, err := inspector.TableNames(ctx)
	if err != nil {
		fmt.Printf("failed to list table names: %s\n", err)
		return
	}
	for _, ref := range refs {
		fmt.Printf("%s.%s\n", ref.Schema, ref.Name)
	}

	// Output:
	// main.armadillos
	// main.zebras
	// tenant.zebras
}
```
source: [examples/inspect_sqlite_table_names_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/inspect_sqlite_table_names_example_test.go)
<!-- END INCLUDE -->

`inspect.New` takes the same kind of handle as `rasql.New` plus the dialect that describes the database being read. The result is an ordinary descriptor, so it can be validated, compared against a checked-in definition, or handed to the generator. A PostgreSQL `NUMERIC(p,s)` or MySQL `DECIMAL(p,s)` column normalizes to `schema.DecimalType` with `Precision` and `Scale` filled in from the catalog. Catalog metadata comes from whichever server the application points at, so a decimal is recognized only from a type declaration matched in full, never from a substring of one: on MySQL, `COLUMN_TYPE` must read exactly `DECIMAL` or `NUMERIC`, optionally followed by `(precision)` or `(precision, scale)`, and catalog text such as `FOODECIMALBAR` is an unsupported type rather than a decimal. Four decimal shapes return an error rather than a descriptor: a PostgreSQL column declared as bare, unconstrained `numeric` has no precision the catalog can report, so `Table` refuses it rather than guess one; a decimal column whose catalog row reports no scale is refused for the same reason, since recording the missing scale as 0 would drop the column's fractional digits; a MySQL `DECIMAL`/`NUMERIC` declaration carrying `UNSIGNED`, `ZEROFILL` or any other modifier is refused, because `DecimalType` cannot record the modifier and re-rendering the column without it would change the values the column permits; and any SQLite `DECIMAL`/`NUMERIC` column is refused outright, since such a column actually holds `REAL` values in SQLite (see [Logical column types](#logical-column-types) above) and `schema.DecimalType` would claim an exactness the stored data does not have. A SQLite column that rasql itself created as `DecimalType` was declared `TEXT`, and inspects back as `schema.TextType`, not `schema.DecimalType`: SQLite's catalog does not record enough to recover the original logical type.

Integer declarations are matched the same way, and for the same reason. On MySQL, `COLUMN_TYPE` must read exactly `TINYINT`, `SMALLINT`, `MEDIUMINT`, `INT`, `INTEGER` or `BIGINT`, optionally followed by a display width and then by `UNSIGNED` and/or `ZEROFILL`; a declaration carrying `UNSIGNED` sets `IntegerType.Unsigned`, a stated display width sets `IntegerType.DisplayWidth`, and `ZEROFILL` sets `IntegerType.ZeroFill` (see [Integer display width and ZEROFILL](#integer-display-width-and-zerofill)); any other trailing modifier is still refused, since the concrete type cannot record it. `ZEROFILL` always implies `UNSIGNED` in MySQL, and the catalog always spells the two together in that order, so a bare `ZEROFILL` with no preceding `UNSIGNED` is not a shape MySQL's own catalog produces and is refused rather than guessed at. Matching the whole declaration is what makes `UNSIGNED` visible at all: a substring test on `INT` cannot see what follows the type, which is how a `bigint(20) unsigned` column used to inspect as a plain signed integer and re-render as `BIGINT`, losing every value above 9223372036854775807. It also accepted MySQL's `POINT`, which is not an integer at all and is now an unsupported type. PostgreSQL has no unsigned integer type and SQLite stores a signed 64-bit value whatever a column is declared, so neither ever reports an unsigned column; a SQLite column declared `UNSIGNED BIG INT` inspects as the signed integer column it really is.

MySQL text declarations preserve a stated width the same way: `CHAR(n)` and `VARCHAR(n)` normalize to `schema.TextType` with `Width` set to `n`, matched as a whole declaration for the same reason, so a modifier such as `ZEROFILL` after the width is refused rather than silently dropped. `CHAR` also sets `schema.TextType.Fixed`, since `COLUMN_TYPE` distinguishes it from `VARCHAR`; re-rendering the column (see [Text column width](#text-column-width) above) reproduces the same `CHAR(n)` rather than widening it to `VARCHAR(n)`. `TEXT`, `ENUM`, and `SET` all normalize to `schema.TextType` too, but with no width stated: MySQL never reports `TEXT` as `TEXT(n)`, and `ENUM`/`SET` carry a value list `schema.TextType` has nowhere to record, so both were already lossy round-trips before `Width` existed and remain so.

MySQL has no UUID type, so a column declared to hold one is a hand-written `CHAR(36)` and inspects exactly like any other fixed-width `CHAR(n)` column: as `schema.TextType{Width: 36, Fixed: true}`, not `schema.UUIDType`. Its catalog row is indistinguishable from a `CHAR(36)` column that was never meant to hold a UUID, so `inspect` cannot and does not guess otherwise. That still round-trips through `render.CreateTable` back to `CHAR(36)`, which is what stops `migrate/diff` from reporting a phantom change on a UUID column; it just does not recover the original `schema.UUIDType`.

PostgreSQL preserves a stated width too, but from a different catalog column: `data_type` never carries a length the way MySQL's `COLUMN_TYPE` does, so `CHARACTER VARYING(n)` and `CHARACTER(n)` read their width from `information_schema.columns.character_maximum_length` instead, which is `NULL` for `TEXT` and for an unbounded `CHARACTER VARYING` and otherwise the stated length. Bare `CHARACTER` means `CHARACTER(1)` and reports a length of 1, not `NULL`. `data_type` also distinguishes `character` from `character varying`, so `CHARACTER(n)` normalizes with `schema.TextType.Fixed` set and re-renders as `CHAR(n)`, the PostgreSQL counterpart to MySQL's `CHAR` handling above.

For PostgreSQL and SQLite, `Table` never returns a descriptor silently missing columns or a primary key. PostgreSQL's `information_schema` views are filtered by the inspecting role's privileges, while `pg_catalog` is not, so `inspect` reads the true column count and the primary key from `pg_catalog` rather than trusting `information_schema` alone. A role whose grants hide some or all of a table's columns gets `inspect.IncompleteMetadataError`, and a name that does not exist gets `inspect.TableNotFoundError`. A plain read-only role gets its primary key from `pg_catalog` too, so it sees a complete descriptor with no error. MySQL filters `information_schema.columns` by column privileges, so `inspect` cross-checks the visible column count against the full `SHOW CREATE TABLE` definition and returns `inspect.ErrIncompleteMetadata` when a restricted grant hides columns. SQLite has no privilege filtering.

## Next

[Querying](03-querying.md) reads rows through these descriptors, or [Writing rows](04-writing.md) puts rows into them.
