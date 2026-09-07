# Migrations

`rasql migrate` applies checked-in SQL migrations, reverts them, and records every completed migration with a SHA-256 checksum. It supports PostgreSQL, MySQL, and SQLite. PostgreSQL and SQLite apply each migration atomically. MySQL DDL may commit before a migration record is written, so the runner records progress and blocks uncertain work until it is reconciled.

Run the command outside the application, before it starts. The application then opens a database whose schema is already in place.

Install the command once:

```sh
go install github.com/lestrrat-go/rasql/cmd/rasql@latest
```

Then run `rasql migrate <command> [flags]`. The standalone `rasqlmigrate` binary still accepts the same commands under its own name.

## Migration directories

One application keeps one migration root. Each first-level directory in it is one migration ID, and it holds a `.up.sql` source for every step forward with the `.down.sql` that undoes it beside it.

```text
db/migrations/
  001_initial/
    001_create_users.up.sql
    001_create_users.down.sql
    002_users_email_index.up.sql
    002_users_email_index.down.sql
  002_add_user_nickname/
    001_add_nickname.up.sql
    001_add_nickname.down.sql
```

Give each engine its own root, such as `db/migrations/postgresql` and `db/migrations/sqlite`, only when the same application really ships DDL for more than one engine. Two engines mean two histories that must not share a root, and each `rasql migrate` run then names the one it is for with `-dir`. An application on a single engine needs no such level.

Each `.sql` file contains one native database statement. `rasql migrate` sends its bytes unchanged to the database driver. It does not parse, split, or render SQL, so a migration can use the database's own DDL syntax.

Forward migration IDs, source filenames, source order, and source bytes are part of the recorded checksum. Do not edit, rename, move, add to, or remove the forward sources of an applied migration. Create a new migration directory for every later change, or revert the migration first with [`down`](#revert-a-migration).

Reverse sources stay out of the checksum, so a `.down.sql` can be added or corrected for a migration that is already applied.

### The rules a migration root follows

Create these directories yourself with `mkdir`. There is no command that scaffolds them, because the layout is the whole contract and every rule below is enforced where the migrations are read and applied.

- The root passed as `-dir` holds directories and nothing else. A file sitting directly in it fails the load, so a stray `notes.txt` is reported rather than skipped.
- A directory's name is the migration ID that the history table records. It may be any non-empty valid UTF-8 text up to 255 bytes, with no NUL.
- Migrations run in the sort order of those names, compared as bytes rather than as numbers. `10_x` sorts before `9_x`, so pad the numbers: `001`, `002`, `010`.
- A migration directory holds sources and no subdirectories. Every source ends in `.up.sql` or `.down.sql`. Any other name fails the load, a plain `.sql` included, which is what turns a misspelled `001_add_nickname.dwon.sql` into an error rather than a silent extra forward source.
- A migration's forward sources run in ascending filename order, with the same byte comparison and the same need for padding.
- Its reverse sources run in **descending** filename order, so the migration is undone in the reverse of the order it was done.
- Every migration needs at least one `.up.sql` and either matching `.down.sql` sources or a `.rasql-irreversible` marker with a reason. A marked migration applies but cannot be reverted. A change that destroys data still writes the reverse that rebuilds the structure when one is proven safe, such as re-adding a dropped column without its rows.
- A hand-written migration may hold fewer reverse sources than forward ones. One `DROP TABLE` can undo a create-table plus a create-index without an empty file standing in for the second.
- Every `.down.sql` must share its stem with a `.up.sql` in the same migration. That is the typo check, and it is the only pairing rule.
- An entry whose name starts with a dot is ignored, except `.rasql-irreversible`, which marks a migration as intentionally irreversible. Other dot files remain ignored, so an editor's swap file does not become a migration.
- A source file must hold something other than whitespace.

The engine enforces the rest at apply time, against the history table rather than the disk. A migration whose recorded bytes no longer match its forward files fails with a checksum error. A new migration whose name sorts before one that is already applied fails as "recorded after a missing migration", rather than running out of order or being skipped. A recorded migration whose directory has since disappeared fails as "was not supplied".

## Create and review a migration

Schema diffs show typed operations and decisions before publication. Supply repeatable `-backfill decision-id=sql-file` and `-rename decision-id=baseline-column` flags to `diff` or `diff-live`. Unresolved plans cannot create an output directory, so review every generated forward and reverse source before applying it.

PostgreSQL and MySQL caller-supplied native backfills are irreversible. Review a separate no-backfill round trip when a reversible migration is required. SQLite groups supported changes into a rebuild and writes the safe reverse order after live catalog facts are attached.

<!-- INCLUDE(examples/schema_evolution_example_test.go#schemaEvolution) -->
```go
ctx := context.Background()
if err := os.MkdirAll(".tmp", 0o700); err != nil {
	fmt.Println(err)
	return
}
root, err := os.MkdirTemp(".tmp", "schema-evolution-*")
if err != nil {
	fmt.Println(err)
	return
}
defer func() { _ = os.RemoveAll(root) }()
database, err := sql.Open("sqlite", filepath.Join(root, "schema.sqlite"))
if err != nil {
	fmt.Println(err)
	return
}
defer func() { _ = database.Close() }()
if _, err := database.ExecContext(ctx, "CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT NOT NULL)"); err != nil {
	fmt.Println(err)
	return
}
if _, err := database.ExecContext(ctx, "INSERT INTO members (name) VALUES ('Ada')"); err != nil {
	fmt.Println(err)
	return
}
connection, err := database.Conn(ctx)
if err != nil {
	fmt.Println(err)
	return
}
defer func() { _ = connection.Close() }()
analyzer := sqlite.New()
inspector, err := inspect.New(connection, dialect.SQLite())
if err != nil {
	fmt.Println(err)
	return
}
table, err := inspector.Table(ctx, "members")
if err != nil {
	fmt.Println(err)
	return
}
baselineSources, err := analyzer.LiveSources(table)
if err != nil {
	fmt.Println(err)
	return
}
baseline, err := analyzer.Parse(baselineSources)
if err != nil {
	fmt.Println(err)
	return
}
facts, err := sqlite.InspectLiveCatalog(ctx, connection, "members")
if err != nil {
	fmt.Println(err)
	return
}
baseline, err = analyzer.AttachLiveCatalog(baseline, facts)
if err != nil {
	fmt.Println(err)
	return
}
target, err := analyzer.Parse([]diff.Source{{Path: "schema.sql", SQL: sqltext.Text("CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT NOT NULL, email TEXT NOT NULL)")}})
if err != nil {
	fmt.Println(err)
	return
}
plan, err := analyzer.Diff(baseline, target)
if err != nil {
	fmt.Println(err)
	return
}
for _, operation := range plan.Operations {
	fmt.Printf("operation %s (%s): %s\n", operation.ID, operation.Kind, operation.Summary)
}
for _, decision := range plan.Decisions {
	fmt.Printf("decision %s (%s): %s\n", decision.ID, decision.Kind, decision.Reason)
}
fmt.Println("executable:", plan.Executable())
if len(plan.Decisions) != 1 {
	fmt.Printf("expected one decision, got %d\n", len(plan.Decisions))
	return
}
resolved, err := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, BackfillSQL: "UPDATE members SET email = name || '@example.test' WHERE email IS NULL;"})
if err != nil {
	fmt.Println(err)
	return
}
if err := diff.WriteMigration(filepath.Join(root, "migrations", "001_schema_evolution"), resolved); err != nil {
	fmt.Println(err)
	return
}
migrations, err := migrationdir.Load(filepath.Join(root, "migrations"))
if err != nil {
	fmt.Println(err)
	return
}
for _, statement := range migrations[0].Statements {
	fmt.Printf("up: %s %q\n", statement.Source, statement.SQL)
}
for _, statement := range migrations[0].Down {
	fmt.Printf("down: %s %q\n", statement.Source, statement.SQL)
}
```
source: [examples/schema_evolution_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/schema_evolution_example_test.go)
<!-- END INCLUDE -->

Create the directory and add numbered SQL source files:

```sh
mkdir -p db/migrations/002_add_user_nickname
```

For example, `db/migrations/002_add_user_nickname/001_add_nickname.up.sql` can contain:

```sql
ALTER TABLE "users" ADD COLUMN "nickname" TEXT;
```

and `001_add_nickname.down.sql` beside it:

```sql
ALTER TABLE "users" DROP COLUMN "nickname";
```

Review the ordered sources without opening a database connection:

```sh
rasql migrate plan \
  -dir db/migrations
```

## Apply and check migrations

Use `apply`, `status`, and `verify` in local automation and CI. Keep the DSN in an environment variable or secret store, never in a SQL source file.
For SQLite, store the database file outside the migration root.

```sh
rasql migrate apply \
  -dir db/migrations \
  -dialect sqlite \
  -dsn "$DATABASE_URL"

rasql migrate status \
  -dir db/migrations \
  -dialect sqlite \
  -dsn "$DATABASE_URL"

rasql migrate verify \
  -dir db/migrations \
  -dialect sqlite \
  -dsn "$DATABASE_URL"
```

`apply` runs every pending migration, oldest first, and prints one `applied<TAB>ID` line per migration followed by a count. Pass `-to ID` to stop at a chosen migration, which applies `ID` and every pending migration before it and leaves the rest pending. Naming a migration that is already applied applies nothing. Pass `-dry-run` to print the forward SQL the run would execute without running it. That dry run reads the history table, so it prints only what is still pending, while [`plan`](#create-and-review-a-migration) prints every supplied source and never opens a database.

`status` reports `applied`, `pending`, `changed`, `out_of_order`, `unknown`, and `incomplete` migrations. `verify` succeeds only when every supplied migration is `applied`. The command redacts the exact DSN from returned errors. Pass `-history-table` to each database command when the default `rasql_schema_migrations` table name conflicts with an existing application table.

If MySQL stops during a migration, `status` shows the source and direction that need review. Use a read-only query returning one non-NULL boolean to reconcile it after checking the database:

```sh
rasql migrate reconcile \
  -dir db/migrations -dialect mysql -dsn "$DATABASE_URL" \
  -id 20240901_add_owner -check 'SELECT EXISTS (SELECT 1 FROM owners WHERE id = 1)'
```

`true` records the source as executed and `false` removes its pending intent so the source can run later. The command never executes migration SQL or accepts a force outcome.

## Revert a migration

`rasql migrate revert` undoes applied migrations, newest first, running each one's `.down.sql` sources and deleting its history record. It requires exactly one of `-to` and `-steps`, so a run that names no target reverts nothing rather than choosing a depth for you.

```sh
rasql migrate revert \
  -dir db/migrations \
  -dialect sqlite \
  -dsn "$DATABASE_URL" \
  -steps 1

rasql migrate revert \
  -dir db/migrations \
  -dialect sqlite \
  -dsn "$DATABASE_URL" \
  -to 001_initial
```

`-steps N` reverts the N newest applied migrations, and it has no counterpart on `apply`, which counts forward from a named migration instead. `-to ID` leaves the database at the point where `ID` was applied, so `ID` itself stays applied and every migration after it is reverted. Naming the newest applied migration reverts nothing. Add `-dry-run` to print the reverse SQL the run would execute without opening a transaction.

A reverted migration becomes `pending` again, so `apply` runs it once more. That is the fix-and-reapply loop: revert the migration, correct its sources, apply it again.

The whole run is refused, before any statement runs, when a selected migration's forward sources no longer match their recorded checksum, when `-to` names a migration that is not applied, when `-steps` exceeds the number applied, or when the history disagrees with the supplied migrations. A refused run changes nothing.

PostgreSQL and SQLite revert a migration atomically, so a failed revert leaves the database as it was. MySQL commits DDL implicitly, so a revert that fails partway retains a progress row and blocks replay until reconciliation. Both behaviors are pinned by live tests in `migrate/revert_integration_test.go`.

## Generate PostgreSQL, MySQL, and SQLite migrations

`rasql migrate diff` compares two PostgreSQL, MySQL, or SQLite desired-schema directories without connecting to a database. Each directory holds supported natural DDL: `CREATE TABLE` statements and named `CREATE INDEX` statements. It parses source files recursively, then prints a proposed raw SQL migration for review.

```text
db/schema/
  postgresql-v1.1/
    tables/members.sql
  postgresql-v1.2/
    tables/members.sql
db/migrations/
  postgresql/
```

Preview the generated migration before writing it:

```sh
rasql migrate diff \
  -dialect postgresql \
  -from db/schema/postgresql-v1.1 \
  -to db/schema/postgresql-v1.2
```

Pass a new migration directory to write one SQL source file for each generated statement:

```sh
rasql migrate diff \
  -dialect postgresql \
  -from db/schema/postgresql-v1.1 \
  -to db/schema/postgresql-v1.2 \
  -output db/migrations/postgresql/002_add_member_email
```

Use the same workflow for MySQL with `-dialect mysql` and MySQL schema and migration directories:

```sh
rasql migrate diff \
  -dialect mysql \
  -from db/schema/mysql-v1.1 \
  -to db/schema/mysql-v1.2 \
  -output db/migrations/mysql/002_add_member_email
```

Use the SQLite dialect with separate SQLite schema and migration directories:

```sh
rasql migrate diff \
  -dialect sqlite \
  -from db/schema/sqlite-v1.1 \
  -to db/schema/sqlite-v1.2 \
  -output db/migrations/sqlite/002_add_member_email
```

The first PostgreSQL, MySQL, and SQLite slices generate new tables, new nullable columns, new required columns with defaults, and ordinary named indexes. They refuse to infer renames, removals, changed columns or constraints, and required columns without a backfill. PostgreSQL `CREATE INDEX CONCURRENTLY` generates an explicit nontransactional migration. The SQLite adapter requires every added default to be literal and refuses added primary-key, unique, generated, and foreign-key-default columns. Write those migrations by hand.

The generated files use the normal migration format. Review them, then apply them with `rasql migrate apply`. Do not edit a generated migration after it has been applied.

## Compare one live table with a desired schema

`rasql migrate diff-live` takes the baseline from a running database instead of a checked-in directory. It inspects one table, renders that descriptor back into DDL, and compares it with the desired schema named by `-to`. PostgreSQL, MySQL, and SQLite are supported.

Preview the migration the comparison proposes:

```sh
rasql migrate diff-live \
  -dialect postgresql \
  -dsn "$DATABASE_URL" \
  -table members \
  -to db/schema/postgresql-v1.2
```

Pass `-output` to write the proposed statements as a new migration directory instead of printing them:

```sh
rasql migrate diff-live \
  -dialect postgresql \
  -dsn "$DATABASE_URL" \
  -table members \
  -to db/schema/postgresql-v1.2 \
  -output db/migrations/postgresql/002_add_member_email
```

`-dialect`, `-dsn`, `-table`, and `-to` are all required. The command reads the whole comparison inside one transaction, rolls that transaction back, and reports `no schema changes` when the live table already matches. It refuses a plan whose statements touch any table other than `-table`, and it applies the same restrictions the `diff` command does, so a rename, a removal, or a changed column is still written by hand. The command redacts the exact DSN from returned errors.

Any live-schema comparison that uses `inspect` requires complete metadata privileges. MySQL filters `information_schema.columns` by column privileges, so a partial grant can create a false baseline. Use table- or database-level `SELECT` (or equivalent full column visibility) before running `diff-live`. The inspector returns `inspect.ErrIncompleteMetadata` when the visible column count does not match the full `SHOW CREATE TABLE` definition.

## Dump a live schema

`rasql migrate dump` sweeps a live PostgreSQL, MySQL, or SQLite database and writes rasql's own schema descriptor for it, ordered so the result replays. It is not a faithful `pg_dump` or `mysqldump`: it writes the same DDL `diff-live` already builds for one table, swept over every table `-table`/`-exclude` select and ordered by foreign key so a table is created after every other table in the dump it references.

Run it with `-format schema` to write one file per table, the directory `migrate diff -from`/`-to` reads:

```sh
rasql migrate dump \
  -dialect postgresql \
  -dsn "$DATABASE_URL" \
  -output db/schema/postgresql-v1
```

That leaves one `<table>.sql` file per table (`<schema>__<table>.sql` when the table is schema-qualified), each holding the table's `CREATE TABLE` statement and then its `CREATE INDEX` statements:

```text
db/schema/postgresql-v1/
  teams.sql
  members.sql
  audits.sql
```

Run it with `-format migration` to write a numbered migration directory instead, ready for `rasql migrate apply`:

```sh
rasql migrate dump \
  -dialect postgresql \
  -dsn "$DATABASE_URL" \
  -format migration \
  -output db/migrations/postgresql/001_initial
```

That leaves one `.up.sql`/`.down.sql` pair per `CREATE TABLE` statement and one `.up.sql`/`.down.sql` pair per generated `CREATE INDEX` statement, numbered in the same dependency order:

```text
db/migrations/postgresql/001_initial/
  001_create_teams.up.sql
  001_create_teams.down.sql
  002_create_members.up.sql
  002_create_members.down.sql
  003_create_index_members_team_id_idx.up.sql
  003_create_index_members_team_id_idx.down.sql
```

`-dialect` and `-dsn` are required. `-table` names a comma-separated list of tables to dump instead of every base table; `-exclude` names tables to skip during a sweep, and is refused together with `-table`. `-history-table` names a migration history table a sweep skips (`rasql_schema_migrations` by default). `-format` is `schema` or `migration` (`schema` by default). `-timeout` bounds the whole run (`30s` by default). Omit `-output` to preview the files a run would write, headed by their own path, without writing anything to disk. The command reads the whole sweep inside one read-only transaction, rolls it back, and redacts the exact DSN from returned errors. `-output` must be missing or empty; a dump never overwrites checked-in DDL, and there is no `-force` flag.

A dump refuses, by table and column, whatever it cannot capture faithfully, rather than writing DDL that builds a different database than the one it read: a PostgreSQL identity column whose sequence departs from `bigint`'s own defaults (start 1, increment 1, minimum 1, maximum 9223372036854775807, cycle `NO`), and a MySQL string default `information_schema` reports unquoted that rasql cannot safely re-quote. A PostgreSQL identity column with a default sequence, and a MySQL `AUTO_INCREMENT` column, both dump and replay normally: `render` emits `GENERATED ALWAYS AS IDENTITY`/`GENERATED BY DEFAULT AS IDENTITY` or `AUTO_INCREMENT` for either, but a non-default sequence would be silently dropped, since the rendered clause never states a `START WITH` or `INCREMENT BY`. It also refuses any column whose declared type is not on a short, per-dialect allow-list of types this repository has verified render back exactly as declared -- PostgreSQL's `bigint`, `boolean`, `text`, `character varying`, `character`, `numeric`, `double precision`, `timestamp with time zone`, `bytea`, `uuid`, and `jsonb`; MySQL's `bigint` (optionally `unsigned`), `text`, `varchar`, `char`, `decimal`, `double`, `datetime`, `blob`, `json`, and a column declared `boolean` (MySQL's own `tinyint(1)`); and SQLite's `INTEGER`, `TEXT`, `REAL`, and `BLOB`. A `smallint`, an `integer`, a PostgreSQL `date` or `timestamp`, a MySQL `float`, or a SQLite `VARCHAR(n)` all fall outside those lists today, because rasql's schema model cannot state the difference between them and the type it would render instead, and dumping one anyway would build a column that quietly means something else. A foreign-key cycle between two tables in the same dump is refused too, since no `CREATE TABLE` order can satisfy it; write that migration by hand. `render.CreateTable` and `render.CreateIndexes`'s own refusals -- a generated column, an expression index, and the rest -- pass through unchanged; a partial index now dumps and replays on PostgreSQL and SQLite the same way, since `render` renders its `WHERE` clause on both.

Not carried by a dumped identity column, the same way it is not carried for a `BIGSERIAL` column today: the identity sequence's `CACHE` setting (`pg_sequence.seqcache`, not exposed through `information_schema`) and the sequence's current value.

The one PostgreSQL rewrite a dump does make is turning a sequence-backed default into a plain `BIGSERIAL` column. The sequence itself is never part of what a dump writes, so the original `DEFAULT nextval(...)` text would otherwise reference a sequence the run never created. This reaches a `BIGSERIAL` column and a `bigint` column wired to a sequence by hand; a `SERIAL` or `SMALLSERIAL` column reports `integer` or `smallint` to the catalog and so is already refused by the type allow-list above.

## Go API

`migrate.Runner` is available when an application needs to embed migration execution in a separate administrative program. Each `migrate.Statement` holds a source name and native SQL text. Supply the complete migration set to `Runner.Apply` with a target built by `migrate.AllPending` or `migrate.ApplyThrough`. It orders migrations by ID, returns what it applied, and rejects duplicate IDs, missing recorded migrations, skipped migrations, and changed source checksums. `Runner.ApplyPlan` reports the same selection without running it, and `Runner.Revert` and `Runner.RevertPlan` mirror both with `migrate.Through` and `migrate.Steps`.

The runner does not infer migrations from Go table descriptors, compare live schemas, or repair a database automatically. The PostgreSQL diff command compares checked-in desired-schema sources instead. `inspect` can still compare supported metadata outside the migration runner.
