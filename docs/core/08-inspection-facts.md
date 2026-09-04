# Inspection-only facts

`inspect.Table` reads a table out of a live database and records everything the
catalog reports about it, including facts `rasql` cannot write back as DDL. This
page lists those facts, one section per group.

Read [Schemas](01-schema.md) first. It covers the parts of a descriptor an
application writes for itself, and none of the facts here has an option-form
constructor, so each one reaches a descriptor only when `inspect` produced it.
`TableDef.Validate` accepts every fact on this page. `render.CreateTable`,
`render.CreateIndexes`, and the migrate diff-live path refuse to build DDL for
one, and each section names the error they return.

## Unique constraint facts

`UniqueDef` carries ten facts a live database can attach to a unique constraint beyond a plain column list: `Deferrable`, `NullsNotDistinct`, `IncludeColumns`, `OnConflict`, `Keys`, `Temporal`, `StorageParameters`, `Tablespace`, `ReplicaIdentity`, and `Collations`. `inspect.Table` records all ten.

`Deferrable` reuses `schema.Deferrability`, the same type `ForeignKeyDef.Deferrable` uses (see [Foreign key facts](#foreign-key-facts)). Its zero value, the empty string, means `NOT DEFERRABLE`.

`NullsNotDistinct` marks a PostgreSQL 15+ `UNIQUE NULLS NOT DISTINCT` constraint, under which two `NULL`s in the constrained columns conflict with each other instead of coexisting. Its zero value, `false`, means `NULLS DISTINCT`, the SQL default.

`IncludeColumns` lists a PostgreSQL unique constraint's `INCLUDE` columns: columns carried by the constraint's backing index for covering reads without taking part in the uniqueness check itself. Its zero value, `nil`, means the constraint has no `INCLUDE` clause.

`OnConflict` names a SQLite unique constraint's `ON CONFLICT` resolution as a `schema.ConflictResolution`. Its zero value, the empty string, means SQLite's own default resolution, `ABORT` — an explicit `ON CONFLICT ABORT` clause names the same zero value, since it behaves identically to no clause at all. `schema.ConflictRollback`, `schema.ConflictFail`, `schema.ConflictIgnore`, and `schema.ConflictReplace` name the other four resolutions SQLite defines.

`Keys`, when non-empty, is the full ordered list of per-key facts for a SQLite `UNIQUE` constraint that orders at least one key `DESC` or names a non-default collation on it, reusing `IndexKeyDef` — the same type `IndexDef.Keys` uses (see [Index key details](#index-key-details)). When `Keys` is set it replaces `Columns` as the source of the constraint's key order, the same way `IndexDef.Keys` replaces `IndexDef.Columns`. `IndexKeyDef.Expression` is always a bare column name here, never expression text: SQLite's own grammar prohibits an expression inside a `UNIQUE` table constraint (only a column reference, optionally `COLLATE`d and ordered, is allowed), so `inspect.Table` still refuses a `UNIQUE` constraint that carries one, which a hand-supplied definition can reach rasql's parser with even though a live SQLite catalog never produces it. `IndexKeyDef.OperatorClass`, `.PrefixLength`, and `.NullsOrder` never come from a SQLite descriptor, the same as they never come from a SQLite `IndexDef.Keys`. Its zero value, `nil`, means every key is a plain ascending column in the default collation, described by `Columns` exactly as before this field existed. `TableDef.Validate` rejects a `UniqueDef` that sets both `Columns` and `Keys`.

`Temporal` marks a PostgreSQL 18+ temporal unique constraint declared with `WITHOUT OVERLAPS` on its last column. Its zero value, `false`, means the constraint is an ordinary, non-temporal `UNIQUE` constraint.

`StorageParameters`, `Tablespace`, and `ReplicaIdentity` record the same three facts about a named unique constraint's own backing index that `IndexDef.StorageParameters`, `.Tablespace`, and `.ReplicaIdentity` record about a plain index (see [Index validity, storage, and placement facts](#index-validity-storage-and-placement-facts)): storage parameters such as `fillfactor` from a `WITH (...)` clause, a nondefault tablespace, and whether the index serves as the table's `REPLICA IDENTITY USING INDEX`. Each keeps the same zero value and the same meaning as its `IndexDef` counterpart.

`Collations` records a named unique constraint's backing index's non-default per-column collation, keyed by column name and valued by the collation exactly as the server reports it. A column missing from the map uses its own default collation. Its zero value, `nil`, means every column of the constraint uses its own default collation.

`render.CreateTable` and the migrate diff-live path refuse to build DDL for a unique constraint naming any of the ten, returning a `*render.UnsupportedUniqueDeferrabilityError`, `*render.UnsupportedUniqueNullsNotDistinctError`, `*render.UnsupportedUniqueIncludeColumnsError`, `*render.UnsupportedUniqueConflictResolutionError`, `*render.UnsupportedUniqueKeyDetailsError`, `*render.UnsupportedUniqueTemporalError`, `*render.UnsupportedUniqueStorageParametersError`, `*render.UnsupportedUniqueTablespaceError`, `*render.UnsupportedUniqueReplicaIdentityError`, or `*render.UnsupportedUniqueCollationsError` that names the constraint and, where relevant, the fact it named.

SQLite inspection still refuses a `UNIQUE` constraint with an expression, collation, or ordering. That stays out of scope for now.

## SQLite table-level options

`TableDef` carries four SQLite table-level facts beyond its columns and primary key: `Strict`, `WithoutRowID`, `PrimaryKeyAutoincrement`, and `PrimaryKeyOnConflict`. `inspect.Table` records all four.

`Strict` marks a SQLite `STRICT` table: every column must declare one of SQLite's strict type names, and a value that does not match a column's type is rejected instead of stored under SQLite's usual type-affinity rules. Its zero value, `false`, means the table is not `STRICT`.

`WithoutRowID` marks a SQLite `WITHOUT ROWID` table, which stores rows keyed directly by their primary key instead of by an implicit rowid. Its zero value, `false`, means the table has the usual SQLite rowid.

`PrimaryKeyAutoincrement` marks a SQLite primary key declared with the `AUTOINCREMENT` keyword, which changes SQLite's rowid-allocation algorithm to never reuse a rowid a deleted row once used. It lives on `TableDef` rather than on a column or on `PrimaryKey`'s own string list, because `AUTOINCREMENT` is a property of the table's single `INTEGER PRIMARY KEY` declaration rather than of any one column definition. Its zero value, `false`, means the primary key carries no `AUTOINCREMENT` keyword.

`PrimaryKeyOnConflict` names a SQLite primary key's `ON CONFLICT` resolution, reusing `schema.ConflictResolution` — the same type `UniqueDef.OnConflict` uses (see [Unique constraint facts](#unique-constraint-facts)). Its zero value, the empty string, means SQLite's own default resolution, `ABORT`.

`PrimaryKeyAutoincrement` and `PrimaryKeyOnConflict` are both meaningless on a table with no primary key at all, so `TableDef.Validate` rejects either one set on a `TableDef` whose `PrimaryKey` is empty.

`render.CreateTable` and the migrate diff-live path refuse to build DDL for a table naming any of the four, returning a `*render.UnsupportedTableStrictError`, `*render.UnsupportedTableWithoutRowIDError`, `*render.UnsupportedPrimaryKeyAutoincrementError`, or `*render.UnsupportedPrimaryKeyConflictResolutionError` that names the table and, for the last, the conflict resolution it named.

PostgreSQL and MySQL have no `STRICT`, `WITHOUT ROWID`, or SQLite-style primary key `AUTOINCREMENT`/`ON CONFLICT` concept (MySQL's own `AUTO_INCREMENT` is a column-level option, unrelated to `PrimaryKeyAutoincrement`), so none of the four ever comes from a PostgreSQL or MySQL descriptor. SQLite inspection still refuses a `CREATE TABLE AS SELECT` definition and a table kind other than a plain table, a virtual table, or a virtual table's own shadow table (see [SQLite virtual tables](#sqlite-virtual-tables)). Those stay out of scope for now.

## SQLite virtual tables

`TableDef.VirtualTableModule` and `.VirtualTableModuleArguments` describe a SQLite `CREATE VIRTUAL TABLE` definition, and `ColumnDef.Hidden` describes a column the table's own module declares hidden. `inspect.Table` records all three. A hidden column is the kind a virtual table module declares, such as `fts5`'s own table-name column, used for `MATCH` filtering, and its `rank` column. A full-text search table is one of the most common ways a SQLite database uses `CREATE VIRTUAL TABLE`, and it always carries hidden columns of exactly this kind.

`VirtualTableModule` names the module a `USING` clause declares, such as `"fts5"` or `"rtree"`. Its zero value, the empty string, means the table is an ordinary table, not a virtual one.

`VirtualTableModuleArguments` lists the module's own arguments, exactly as the `CREATE VIRTUAL TABLE` definition wrote them, one raw text span per argument in declaration order. A module defines its own argument grammar — `fts5`'s own column-definition-like arguments, for instance, which can themselves contain `=` and quoted option values — so `inspect` does not parse into it. Each argument is exactly the source text between its delimiting commas or parentheses, trimmed of leading and trailing space. Its zero value, `nil`, means the module took no arguments, or `VirtualTableModule` is itself empty. It is meaningless, and `TableDef.Validate` rejects it, without a `VirtualTableModule`.

A virtual table has no primary key, table option, unique constraint, check, index, or foreign key that this package can independently verify — those are the module's own business, not something SQLite's table catalog or `PRAGMA` output describes — so `TableDef.Validate` rejects any of `PrimaryKey`, `Strict`, `WithoutRowID`, `UniqueConstraints`, `Checks`, `Indexes`, `ForeignKeys`, or `ExclusionConstraints` stated alongside a non-empty `VirtualTableModule`.

`ColumnDef.Hidden` marks a column a SQLite virtual table module declares hidden: excluded from `SELECT *` and from an `INSERT` that names no column list, but still addressable by name. Its zero value, `false`, means an ordinary column. `TableDef.Validate` rejects a true `Hidden` on a table whose `VirtualTableModule` is empty, since a hidden column only ever comes from a virtual table module.

`render.CreateTable` and the migrate diff-live path refuse to build DDL for a table naming `VirtualTableModule`, returning a `*render.UnsupportedVirtualTableError` that names the table and its module.

PostgreSQL and MySQL have no virtual-table concept, so neither field ever comes from a PostgreSQL or MySQL descriptor. `inspect.Table` also describes a virtual table module's own backing table, which `PRAGMA table_list` calls a `"shadow"` table. `fts5`'s own `_data` table is one. A shadow table describes exactly like an ordinary table, because its own `CREATE TABLE` text is an ordinary definition with no virtual-table facts in it, so it carries no `VirtualTableModule` and no `Hidden` column.

Some virtual table modules, `fts5` among them, quote a shadow table's name with single quotes in that `CREATE TABLE` text. rasql's SQLite parser accepts that form in table-name position, the one place SQLite itself reads a string literal as an identifier, so an `fts5` table's shadow tables describe the same as any other shadow table.

Inspecting an `fts5` table's hidden columns is one half of the picture; the SQL builder's [Full-text search](02-sql-builder.md#full-text-search-sqlite-fts5) section covers the other half, building a `MATCH` predicate and a `bm25` ranking score against a table described this way.

## Index methods

`IndexDef.Method` names a non-default index access method — what PostgreSQL calls an access method and MySQL calls an index type — such as PostgreSQL's `"gin"` or MySQL's `"FULLTEXT"`. Its zero value, the empty string, means the engine's own default, a plain B-tree.

`inspect.Table` records what a live PostgreSQL GIN index or MySQL FULLTEXT index uses, and `TableDef.Validate` accepts it. `render.CreateIndexes` and the migrate diff-live path still refuse to build DDL for one, returning a `*render.UnsupportedIndexMethodError` that names the index and its method.

## Partial and expression indexes

`IndexDef.Predicate` and `IndexDef.Expressions` describe two more facts a real index can carry that `Columns` alone cannot: a `WHERE` condition, and keys that are not plain column references.

`Predicate` is the `WHERE`-clause expression text of a partial index, exactly as the server reports it. Its zero value, the empty string, means the index is not partial.

`Expressions`, when non-empty, is the full ordered list of key expressions for an index that has at least one key that is not a plain column: an expression index, or one that mixes plain columns with expressions. A plain-column key inside it is recorded as its own bare column name, which is itself a valid SQL expression, so an index's full key order always lives in one place instead of being reconstructed by interleaving `Columns` with a second, position-keyed list. `Columns` is left empty whenever `Expressions` is set: an index states its keys as `Columns` or as `Expressions`, never a mix of the two, so `Columns` always holds the ordered column names of a column-only index rather than an ambiguous subset of a mixed index's keys. `TableDef.Validate` rejects an `IndexDef` that sets both.

`inspect.Table` records a live PostgreSQL or SQLite partial index's predicate, and a live PostgreSQL, MySQL, or SQLite expression index's key text, and `TableDef.Validate` accepts both.

`Predicate` is no longer inspection-only: `render.CreateIndexes` renders a non-empty `Predicate` as a `WHERE` clause, appended after the index's key list, verbatim and untranslated, on a dialect with `dialect.CapabilityPartialIndex` — PostgreSQL and SQLite. `Predicate` carries no record of which engine produced it, so `render` cannot compare it against a source dialect; the one decision it can make is whether the target dialect has a partial-index feature at all. On a dialect without the capability, `render.CreateIndexes` and the migrate diff-live path refuse instead, returning a `*render.UnsupportedPartialIndexError` naming the index, its predicate, and the dialect. MySQL has no partial-index concept, so `Predicate` never comes from a MySQL descriptor, and MySQL never has the capability. Rendering a predicate verbatim means a `Predicate` read from one engine and rendered for another is the caller's own responsibility: `render` does not parse, rewrite, or translate the expression text.

`render.CreateIndexes` and the migrate diff-live path still refuse to build DDL for a non-empty `Expressions`, returning a `*render.UnsupportedExpressionIndexError` naming the index and the expressions it carries.

## Index key details

`IndexDef` carries three more facts a live index can attach beyond its key order, method, predicate, and expressions: `IncludeColumns`, `Invisible`, and `Keys`. `inspect.Table` records all three.

`IncludeColumns` lists a PostgreSQL index's `INCLUDE` columns, the index counterpart to `UniqueDef.IncludeColumns` (see [Unique constraint facts](#unique-constraint-facts)): columns carried by the index for covering reads without taking part in its key. Its zero value, `nil`, means the index has no `INCLUDE` clause.

`Invisible` marks a MySQL index the optimizer ignores for query planning without dropping it: MySQL still maintains it on every write and still enforces uniqueness through it. Its zero value, `false`, means the index is visible. PostgreSQL and SQLite have no index-visibility concept, so `Invisible` never comes from a PostgreSQL or SQLite descriptor.

`Keys`, when non-empty, is the full ordered list of per-key facts for an index that has at least one key ordered `DESC`, using a non-default collation or operator class, (MySQL) indexed over a column prefix, or (PostgreSQL) placing `NULL`s first or last against the default its own `ASC`/`DESC` direction implies. These five facts are positional, one per key, so none of them fits `Columns` or `Expressions`, both flat lists with no room to attach a second fact to one position. A set `Keys` becomes the only source of the index's key order, the same way `Expressions` already replaces `Columns` for a mixed or all-expression index. `Columns` and `Expressions` are then both left empty, and each `IndexKeyDef.Expression` carries that key's own text, which is a bare column name for a plain-column key and expression text otherwise. That is what an element of `Expressions` already means. `TableDef.Validate` rejects an `IndexDef` that sets `Keys` alongside `Columns` or `Expressions`.

`IndexKeyDef`'s other fields each keep their own zero value for the ordinary case. `Descending` is `false` for an ascending key, `Collation` and `OperatorClass` are `""` for the default collation and operator class, `PrefixLength` is `0` for a whole column, and `NullsOrder` is `""` for the default nulls placement. An index with no such key still describes its keys with `Columns` or `Expressions` alone, exactly as before `Keys` existed, so every descriptor and checked-in generated file written before this type existed stays valid.

`OperatorClass` and `NullsOrder` are PostgreSQL-only: MySQL and SQLite have no operator class or independent nulls-placement concept, so neither ever comes from a MySQL or SQLite descriptor. `PrefixLength` is MySQL-only: PostgreSQL and SQLite have no index-key-prefix concept, so it never comes from a PostgreSQL or SQLite descriptor. `Collation` comes from PostgreSQL or SQLite. MySQL has no per-key collation concept — its own `COLLATION` catalog column names sort order (`A`/`D`), which `Descending` already captures, not a collating sequence.

`NullsOrder` names a `schema.NullsOrder` — `schema.NullsFirst` or `schema.NullsLast` — recording a key's `NULLS FIRST`/`NULLS LAST` placement, but only when it overrides the placement its own `ASC`/`DESC` direction already implies: `NULLS LAST` for an ascending key, `NULLS FIRST` for a descending key. A key stated with the placement its direction already implies — an ascending key stated `NULLS LAST`, or a descending key stated `NULLS FIRST` — decodes to the same zero value as a key that states no placement at all, since the two are indistinguishable in their effect on sort order. Setting `NullsOrder` forces the index onto `Keys`, the same way `Descending`, `Collation`, `OperatorClass`, or `PrefixLength` does.

`render.CreateIndexes` and the migrate diff-live path still refuse to build DDL for any of the three: a non-empty `IncludeColumns` returns a `*render.UnsupportedIndexIncludeColumnsError`, a true `Invisible` returns a `*render.UnsupportedIndexInvisibleError`, and a non-empty `Keys` — including one whose only nondefault fact is `NullsOrder` — returns a `*render.UnsupportedIndexKeyDetailsError`, each naming the index and, where relevant, what it carries.

## Index validity, storage, and placement facts

`IndexDef` carries five more facts a live PostgreSQL index can report about its own state and physical placement, beyond its key order, method, predicate, expressions, and per-key details: `NotValid`, `StorageParameters`, `Tablespace`, `ReplicaIdentity`, and `NullsNotDistinct`. `inspect.Table` records all five.

`NotValid` records an index a failed `CREATE INDEX CONCURRENTLY` (or similar concurrent operation) left behind: PostgreSQL keeps the index's catalog entry but marks it unusable for lookups or constraint enforcement. Its zero value, `false`, means the index is valid and usable.

`StorageParameters` is a `map[string]string` of the index's storage parameters — settings such as `fillfactor` a `CREATE INDEX ... WITH (...)` clause attaches to an index at creation — keyed and valued exactly as the server reports them. Its zero value, `nil`, means the index carries no storage parameters, PostgreSQL's own default.

`Tablespace` names the tablespace holding the index, or the empty string for the database's default tablespace. Its zero value, `""`, means the default tablespace.

`ReplicaIdentity` marks the index PostgreSQL uses as the table's `REPLICA IDENTITY USING INDEX` for logical replication, in place of the primary key. Its zero value, `false`, means the index is not the replica identity. `TableDef.Validate` rejects a `ReplicaIdentity` index that is not also `Unique`, since PostgreSQL itself requires a replica identity index to be unique.

`NullsNotDistinct` marks a plain (non-constraint) unique index declared `NULLS NOT DISTINCT`, the same fact `UniqueDef.NullsNotDistinct` (see [Unique constraint facts](#unique-constraint-facts)) records for a named unique constraint. Its zero value, `false`, means `NULLS DISTINCT`, the SQL default. `TableDef.Validate` rejects a `NullsNotDistinct` index that is not also `Unique`, since `NULLS NOT DISTINCT` only applies to a unique index.

All five facts are PostgreSQL-only: MySQL and SQLite have no equivalent to any of them, so none ever comes from a MySQL or SQLite descriptor. `render.CreateIndexes` and the migrate diff-live path still refuse to build DDL for any of them: `NotValid` returns a `*render.UnsupportedIndexNotValidError`, a non-empty `StorageParameters` returns a `*render.UnsupportedIndexStorageParametersError`, a non-empty `Tablespace` returns a `*render.UnsupportedIndexTablespaceError`, `ReplicaIdentity` returns a `*render.UnsupportedIndexReplicaIdentityError`, and `NullsNotDistinct` returns a `*render.UnsupportedIndexNullsNotDistinctError`, each naming the index and, where relevant, what it carries.

No PostgreSQL condition on a plain index or its keys causes inspection to reject a table anymore. A key's `NULLS FIRST`/`NULLS LAST` placement that overrides its own `ASC`/`DESC` default is recorded on `IndexKeyDef.NullsOrder` instead (see [Index key details](#index-key-details)), and a named `UNIQUE` constraint whose own backing index carries a non-default collation, storage parameters, tablespace, or replica identity is recorded on `UniqueDef` (see [Unique constraint facts](#unique-constraint-facts)).

A partial index's `Predicate` and an expression key's text are each the server's own re-serialized form, not necessarily the original DDL text: PostgreSQL deparses a partial index's `WHERE` clause and an expression key from the index's stored internal form rather than keeping the original characters around. An index built `WHERE celsius * 9 / 5 + 32 > 100` inspects with `Predicate` equal to `(celsius * 9 / 5 + 32) > 100` — a wrapping pair of parentheses around the arithmetic that the original source never had, added because PostgreSQL's deparser parenthesizes that operand of `>`. That normalization is narrower than the one `ColumnDef.GeneratedExpression` goes through (see [Generated columns](#generated-columns)). PostgreSQL's index predicate and expression route adds parentheses only where its own deparser judges them needed, rather than around every operator. An index built `((celsius * 9 / 5 + 32))` therefore inspects with `Expressions` holding `(celsius * 9 / 5 + 32)`, with no parentheses added around `celsius * 9 / 5` itself.

Either way, `Predicate` and `Expressions` are not guaranteed to match a hand-written migration's exact spacing or parenthesization. SQLite is the exception, the same way it is for `GeneratedExpression`. Its `Predicate` and `Expressions` come from parsing the index's own checked-in `CREATE INDEX` text, so they preserve whatever the source actually wrote.

## Foreign key facts

`ForeignKeyDef` carries seven facts a live database can attach to a foreign key beyond a plain `REFERENCES` clause: `ReferencedSchema` (documented under [Qualify a table with a schema](01-schema.md#qualify-a-table-with-a-schema)), `Match`, `Deferrable`, `NotValid`, `NotEnforced`, `Temporal`, and `DeleteSetColumns`. `inspect.Table` records all seven, and `TableDef.Validate` accepts them.

`MatchType` names a foreign key's `MATCH` clause. Its zero value, the empty string, means `MATCH SIMPLE`, the SQL default. `schema.MatchFull` and `schema.MatchPartial` name the other two forms SQL defines.

`Deferrability` names when the constraint is checked. Its zero value, the empty string, means `NOT DEFERRABLE`. `schema.DeferrableInitiallyImmediate` and `schema.DeferrableInitiallyDeferred` name a deferrable key that checks at the end of each statement by default, or at transaction commit by default, respectively.

`NotValid` and `NotEnforced` are plain `bool` fields rather than named types, because each names a single yes/no fact rather than a choice among several forms the way `Match` and `Deferrable` do: PostgreSQL's `NOT VALID` and `NOT ENFORCED` clauses are either stated or not, with nothing else to name. Both are spelled in the negative, mirroring the SQL keywords themselves, so that the zero value, `false`, reads as "not NOT VALID", which is an ordinary validated, enforced foreign key. A positively named `Validated` and `Enforced` pair would need `true` as its ordinary default, the opposite of every other new field's zero value in this package. `NotValid` records that existing rows were never checked against the foreign key. `NotEnforced` records that PostgreSQL 18+ has stopped enforcing it at all. MySQL foreign keys have no `NOT VALID` or `NOT ENFORCED` concept, so neither field ever comes from a MySQL descriptor.

`Temporal` marks a PostgreSQL 18+ temporal foreign key, declared with `PERIOD` on both the referencing and referenced columns. Its zero value, `false`, means the foreign key is an ordinary, non-temporal `FOREIGN KEY`. MySQL has no temporal-foreign-key concept, so `Temporal` never comes from a MySQL descriptor.

`DeleteSetColumns` lists the subset of `Columns` a PostgreSQL 15+ `ON DELETE SET NULL (columns)` or `ON DELETE SET DEFAULT (columns)` clause names, in the order the server reports them. Its zero value, `nil`, means the `SET NULL`/`SET DEFAULT` action, if stated in `OnDelete`, applies to every column in `Columns`, which is the SQL default. `DeleteSetColumns` is meaningless, and always `nil`, unless `OnDelete` is `schema.SetNull` or `schema.SetDefault`, which `TableDef.Validate` enforces. MySQL has no column-list concept for these actions, so `DeleteSetColumns` never comes from a MySQL descriptor.

`render.CreateTable` and the migrate diff-live path refuse to build DDL for a foreign key naming a non-default `Match` or `Deferrable`, or naming `NotValid`, `NotEnforced`, `Temporal`, or `DeleteSetColumns`, returning a `*render.UnsupportedForeignKeyMatchError`, `*render.UnsupportedForeignKeyDeferrabilityError`, `*render.UnsupportedForeignKeyNotValidError`, `*render.UnsupportedForeignKeyNotEnforcedError`, `*render.UnsupportedForeignKeyTemporalError`, or `*render.UnsupportedForeignKeyDeleteSetColumnsError` that names the foreign key and, where relevant, the fact it named. `ReferencedSchema` is not part of that restriction: both PostgreSQL and MySQL already render it, and SQLite already refuses a genuinely cross-schema one, so a foreign key that only names a different schema renders normally.

SQLite's own introspection cannot report `Match` or `Deferrable` through a PRAGMA the way it reports `ON DELETE`/`ON UPDATE` actions: `PRAGMA foreign_key_list` always reports a foreign key's match as `"NONE"` regardless of what its `REFERENCES` clause actually declared, and reports no deferrability at all. `inspect` reads both from the table's own `CREATE TABLE` text instead. SQLite has no `NOT VALID` or `NOT ENFORCED` concept for foreign keys either, so `NotValid` and `NotEnforced` never come from a SQLite descriptor.

## Check constraint facts

`CheckDef` carries three facts a live database can attach to a check constraint beyond its expression: `NoInherit`, `NotValid`, and `NotEnforced`. `inspect.Table` records all three, and `TableDef.Validate` accepts them. Each is a plain `bool` for the same reason `ForeignKeyDef.NotValid` and `.NotEnforced` are: a yes/no fact rather than a choice among forms, spelled in the negative after its SQL keyword so the zero value, `false`, means the ordinary case: inherited, validated, and enforced.

`NoInherit` records a check constraint declared `NO INHERIT`: a child table in a PostgreSQL inheritance hierarchy neither inherits nor enforces it. `NotValid` records a check constraint declared `NOT VALID`: existing rows were never checked against it. `NotEnforced` records a check constraint declared `NOT ENFORCED`, which both PostgreSQL 18+ and MySQL support. MySQL's `information_schema.table_constraints.enforced` reports this fact directly, which `inspect` now records instead of rejecting, the same way it now handles the PostgreSQL case. MySQL has no `NO INHERIT` or `NOT VALID` concept for check constraints, and SQLite has none of the three, so `NoInherit` and `NotValid` never come from a MySQL descriptor, and none of the three ever comes from a SQLite descriptor.

`render.CreateTable` refuses to build DDL for a check constraint naming any of the three, returning a `*render.UnsupportedCheckNoInheritError`, `*render.UnsupportedCheckNotValidError`, or `*render.UnsupportedCheckNotEnforcedError` that names the check constraint.

## Exclusion constraints

`TableDef.ExclusionConstraints` lists a table's PostgreSQL `EXCLUDE` constraints: a generalization of `UniqueConstraints` that forbids any two rows from having elements that all satisfy their paired operator against each other, rather than always meaning equality. `inspect.Table` records a live PostgreSQL exclusion constraint as a `schema.ExclusionDef`. Its zero value, `nil`, means the table has no exclusion constraints. Exclusion constraints are PostgreSQL-only. MySQL and SQLite have no equivalent concept, so `ExclusionConstraints` never comes from a MySQL or SQLite descriptor.

`ExclusionDef` mixes structured facts with server-reported text, the same split `IndexDef` makes between `Method`/`Predicate` and `Expressions`. `Name` and `Method` are structured: `Method` names the constraint's backing index access method, such as `"gist"`, using the same `schema.IndexMethod` type and the same zero-value convention as `IndexDef.Method` — the empty string means the engine's own default access method (`btree`). `Elements` lists the constraint's elements in declaration order as `schema.ExclusionElementDef` values, each pairing an `Expression` (a bare column name for a plain column element, or the element's expression text otherwise, exactly as the server reports it, the same convention `IndexDef.Expressions` uses) with the `Operator` checked against it, exactly as the server reports it, such as `"="` or `"&&"`. `Predicate` is the `WHERE`-clause expression text of a partial exclusion constraint, on the same convention as `IndexDef.Predicate`. `Deferrable` reuses `schema.Deferrability`, the same type `ForeignKeyDef.Deferrable` and `UniqueDef.Deferrable` use, rather than a second type spelling the same three states.

`TableDef.Validate` accepts an `ExclusionDef`, but `render.CreateTable` and the migrate diff-live path refuse to build DDL for one, returning a `*render.UnsupportedExclusionConstraintError` that names the constraint.

## Generated columns

`ColumnDef.GeneratedExpression` and `ColumnDef.GeneratedStorage` describe a generated column: one whose value the database computes from an expression over other columns, rather than one an `INSERT` or `UPDATE` can write to directly. `inspect.Table` records both on all three engines. A SQLite column that is genuinely hidden, the kind a virtual table module declares, is a different fact, `ColumnDef.Hidden`: see [SQLite virtual tables](#sqlite-virtual-tables).

`GeneratedExpression` is the expression text exactly as the server reports it, or empty for an ordinary column — its zero value. `GeneratedStorage` is a `schema.GeneratedStorage` naming where the value lives. `schema.GeneratedStored` writes the expression's result into the table, and `schema.GeneratedVirtual` computes it each time the column is read. The field means nothing unless `GeneratedExpression` is stated too, so `TableDef.Validate` rejects one without the other. It also rejects a generated column that states `Default`, since such a column takes its value from its expression and SQL does not let the two coexist.

`render.CreateTable` refuses to build DDL for a generated column, returning a `*render.UnsupportedGeneratedColumnError` that names the column, its expression, and its storage kind. Silently rendering a generated column as an ordinary writable one would be a worse failure than most this package refuses, since a generated column cannot be written to at all.

All three engines are covered. SQLite reports a generated column through `PRAGMA table_xinfo`'s hidden flag, with the expression text parsed back out of the table's own `CREATE TABLE` definition. MySQL reports one through `information_schema.columns.EXTRA`, spelled exactly `STORED GENERATED` or `VIRTUAL GENERATED`, alongside `GENERATION_EXPRESSION`. PostgreSQL reports one through `information_schema.columns.is_generated` and `.generation_expression`. It has no storage-kind column in `information_schema`, so `inspect.Table` reads that from `pg_catalog.pg_attribute.attgenerated` instead (`s` for stored, or, from PostgreSQL 18, `v` for virtual — PostgreSQL versions before 18 support only stored generated columns). Before this, PostgreSQL inspection selected no generated-column metadata at all, so a generated column inspected as an ordinary column with an empty default, silently losing the fact that it could not be written to. That gap, not a rejection, is what made a PostgreSQL generated column worth fixing here.

On PostgreSQL and MySQL, `GeneratedExpression` is the server's own re-serialized, normalized form of the expression, not the source text a migration wrote — both engines parse the `GENERATED ALWAYS AS` clause once at `CREATE TABLE` time and report it back from that parsed form, fully parenthesized, rather than keeping the original characters around. A column declared `GENERATED ALWAYS AS (celsius * 9 / 5 + 32) STORED` inspects with `GeneratedExpression` equal to `(((celsius * 9) / 5) + 32)` on PostgreSQL, or `` (((`celsius` * 9) / 5) + 32) `` on MySQL, never the `celsius * 9 / 5 + 32` a person typed. A descriptor `rasqlgen` generates from a live PostgreSQL or MySQL database therefore will not textually match a hand-written migration's expression even when they mean the same thing, and regenerating from that database reproduces the normalized form again, not the original. SQLite is the exception: its `GeneratedExpression` comes from parsing the table's own checked-in `CREATE TABLE` text (see `sqliteGeneratedExpression` in `inspect/inspect.go`), so it preserves whatever the source actually wrote, spacing included.

A generated column changes nothing about code generation: `rasqlgen` still emits an ordinary row field for it, since a generated column reads back like any other column, and the field's Go type follows the same rules as any other column of its logical type. It is only the write path that treats it differently, and automatically: `rasql.Insert`, `rasql.InsertMany`, `rasql.Update`, and `rasql.UpdateMany` all leave a `GeneratedExpression` column out of the column list they build by default, the same way `rasql.UpdateWithOptions` already leaves the primary key out of a plain `Update`'s assignment list, because a database rejects a statement that targets a generated column explicitly. A caller does not need `rasql.DefaultColumns` or `rasql.UpdateColumns` to get this: those options still work for their existing purpose (a database-default or auto-increment column an ordinary, non-generated column happens to have), but naming a generated column through `rasql.UpdateColumns` is refused up front rather than silently accepted or left to fail against the database.

## Next

[Schemas](01-schema.md) covers the descriptor an application writes itself.
[Migrations](07-migrations.md) covers the `diff-live` command that compares a
live table with a desired schema.
