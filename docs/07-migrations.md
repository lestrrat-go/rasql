# Migrations

`rasqlmigrate` applies checked-in, forward-only SQL migrations and records every completed migration with a SHA-256 checksum. It supports PostgreSQL, MySQL, and SQLite. PostgreSQL and SQLite apply each migration atomically. MySQL DDL may commit before a migration record is written, so resolve any failed partial migration before retrying.

The command is designed to run outside the application. The application opens and uses an already-migrated database; it does not import or invoke the migration tool.

Install the command once:

```sh
go install github.com/lestrrat-go/rasql/cmd/rasqlmigrate@latest
```

Then run `rasqlmigrate <command> [flags]`.

## Migration directories

Keep a separate migration root for every database dialect. Each first-level directory is one migration ID, and its `.sql` files run in lexicographic filename order.

```text
db/migrations/
  sqlite/
    001_initial/
      001_create_users.sql
      002_users_email_index.sql
    002_add_user_nickname/
      001_add_nickname.sql
```

Each `.sql` file contains one native database statement. `rasqlmigrate` sends its bytes unchanged to the database driver. It does not parse, split, or render SQL, so a migration can use the database's own DDL syntax.

Migration IDs, source filenames, source order, and source bytes are part of the recorded checksum. Do not edit, rename, move, add to, or remove an applied migration. Create a new migration directory for every later change.

## Create and review a migration

Create an empty migration directory, then add numbered SQL source files:

```sh
rasqlmigrate new \
  -dir db/migrations/sqlite \
  -id 002_add_user_nickname
```

For example, `db/migrations/sqlite/002_add_user_nickname/001_add_nickname.sql` can contain:

```sql
ALTER TABLE "users" ADD COLUMN "nickname" TEXT;
```

Review the ordered sources without opening a database connection:

```sh
rasqlmigrate plan \
  -dir db/migrations/sqlite
```

## Apply and check migrations

Use `apply`, `status`, and `verify` in local automation and CI. Keep the DSN in an environment variable or secret store, never in a SQL source file.
For SQLite, store the database file outside the migration root.

```sh
rasqlmigrate apply \
  -dir db/migrations/sqlite \
  -dialect sqlite \
  -dsn "$DATABASE_URL"

rasqlmigrate status \
  -dir db/migrations/sqlite \
  -dialect sqlite \
  -dsn "$DATABASE_URL"

rasqlmigrate verify \
  -dir db/migrations/sqlite \
  -dialect sqlite \
  -dsn "$DATABASE_URL"
```

`status` reports `applied`, `pending`, `changed`, `out_of_order`, and `unknown` migrations. `verify` succeeds only when every supplied migration is `applied`. The command redacts the exact DSN from returned errors. Pass `-history-table` to each database command when the default `rasql_schema_migrations` table name conflicts with an existing application table.

## Generate PostgreSQL, MySQL, and SQLite migrations

`rasqlmigrate diff` compares two PostgreSQL, MySQL, or SQLite desired-schema directories without connecting to a database. Each directory holds supported natural DDL: `CREATE TABLE` statements and named `CREATE INDEX` statements. It parses source files recursively, then prints a proposed raw SQL migration for review.

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
rasqlmigrate diff \
  -dialect postgresql \
  -from db/schema/postgresql-v1.1 \
  -to db/schema/postgresql-v1.2
```

Pass a new migration directory to write one SQL source file for each generated statement:

```sh
rasqlmigrate diff \
  -dialect postgresql \
  -from db/schema/postgresql-v1.1 \
  -to db/schema/postgresql-v1.2 \
  -output db/migrations/postgresql/002_add_member_email
```

Use the same workflow for MySQL with `-dialect mysql` and MySQL schema and migration directories:

```sh
rasqlmigrate diff \
  -dialect mysql \
  -from db/schema/mysql-v1.1 \
  -to db/schema/mysql-v1.2 \
  -output db/migrations/mysql/002_add_member_email
```

Use the SQLite dialect with separate SQLite schema and migration directories:

```sh
rasqlmigrate diff \
  -dialect sqlite \
  -from db/schema/sqlite-v1.1 \
  -to db/schema/sqlite-v1.2 \
  -output db/migrations/sqlite/002_add_member_email
```

The first PostgreSQL, MySQL, and SQLite slices generate new tables, new nullable columns, new required columns with defaults, and ordinary named indexes. They refuse to infer renames, removals, changed columns or constraints, and required columns without a backfill. The PostgreSQL adapter also refuses `CREATE INDEX CONCURRENTLY`. The SQLite adapter requires every added default to be literal and refuses added primary-key, unique, generated, and foreign-key-default columns. Write those migrations by hand.

The generated files use the normal migration format. Review them, then apply them with `rasqlmigrate apply`. Do not edit a generated migration after it has been applied.

## Compare one live table

Use `rasqlmigrate diff-live` when the current database is the baseline and the desired schema for one table is checked in. The command inspects exactly the table named by `-table`, compares it with the desired-schema directory supplied by `-to`, and prints the same reviewed migration plan as `diff`:

```sh
rasqlmigrate diff-live \
  -dialect sqlite \
  -dsn ./db/application.db \
  -table members \
  -to db/schema/sqlite-members-v2
```

The `-to` directory must contain the desired schema for the selected table. The command does not discover or compare every table in the database. Pass `-output` to write the reviewed plan into a new migration directory.

Inspection is read-only. The command never applies SQL, drops objects, renames objects, or infers destructive changes. Removed tables, columns, indexes, and changed definitions remain errors that require a hand-written migration. Review the printed SQL before applying it, and use a database role with enough metadata privileges for complete inspection; PostgreSQL reports incomplete column visibility instead of guessing.

## Go API

`migrate.Runner` is available when an application needs to embed migration execution in a separate administrative program. Each `migrate.Statement` holds a source name and native SQL text. Supply the complete migration set to `Runner.Apply`; it orders migrations by ID and rejects duplicate IDs, missing recorded migrations, skipped migrations, and changed source checksums.

The runner does not infer migrations from Go table descriptors or repair a database automatically. The desired-schema diff commands generate only the documented additive subset. `diff-live` provides an explicit, one-table live baseline through `inspect`; it still refuses destructive differences and requires review before application.
