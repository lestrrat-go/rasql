# Migrations

`rasql migrate` applies checked-in, forward-only SQL migrations and records every completed migration with a SHA-256 checksum. It supports PostgreSQL, MySQL, and SQLite. PostgreSQL and SQLite apply each migration atomically. MySQL DDL may commit before a migration record is written, so resolve any failed partial migration before retrying.

Run the command outside the application, before it starts. The application then opens a database whose schema is already in place.

Install the command once:

```sh
go install github.com/lestrrat-go/rasql/cmd/rasql@latest
```

Then run `rasql migrate <command> [flags]`. The standalone `rasqlmigrate` binary still accepts the same commands under its own name.

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

Each `.sql` file contains one native database statement. `rasql migrate` sends its bytes unchanged to the database driver. It does not parse, split, or render SQL, so a migration can use the database's own DDL syntax.

Migration IDs, source filenames, source order, and source bytes are part of the recorded checksum. Do not edit, rename, move, add to, or remove an applied migration. Create a new migration directory for every later change.

## Create and review a migration

Create an empty migration directory, then add numbered SQL source files:

```sh
rasql migrate new \
  -dir db/migrations/sqlite \
  -id 002_add_user_nickname
```

For example, `db/migrations/sqlite/002_add_user_nickname/001_add_nickname.sql` can contain:

```sql
ALTER TABLE "users" ADD COLUMN "nickname" TEXT;
```

Review the ordered sources without opening a database connection:

```sh
rasql migrate plan \
  -dir db/migrations/sqlite
```

## Apply and check migrations

Use `apply`, `status`, and `verify` in local automation and CI. Keep the DSN in an environment variable or secret store, never in a SQL source file.
For SQLite, store the database file outside the migration root.

```sh
rasql migrate apply \
  -dir db/migrations/sqlite \
  -dialect sqlite \
  -dsn "$DATABASE_URL"

rasql migrate status \
  -dir db/migrations/sqlite \
  -dialect sqlite \
  -dsn "$DATABASE_URL"

rasql migrate verify \
  -dir db/migrations/sqlite \
  -dialect sqlite \
  -dsn "$DATABASE_URL"
```

`status` reports `applied`, `pending`, `changed`, `out_of_order`, and `unknown` migrations. `verify` succeeds only when every supplied migration is `applied`. The command redacts the exact DSN from returned errors. Pass `-history-table` to each database command when the default `rasql_schema_migrations` table name conflicts with an existing application table.

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

The first PostgreSQL, MySQL, and SQLite slices generate new tables, new nullable columns, new required columns with defaults, and ordinary named indexes. They refuse to infer renames, removals, changed columns or constraints, and required columns without a backfill. The PostgreSQL adapter also refuses `CREATE INDEX CONCURRENTLY`. The SQLite adapter requires every added default to be literal and refuses added primary-key, unique, generated, and foreign-key-default columns. Write those migrations by hand.

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

Any live-schema comparison that uses `inspect` requires complete metadata privileges. MySQL filters `information_schema.columns` by column privileges, so a partial grant can create a false baseline; use table- or database-level `SELECT` (or equivalent full column visibility) before running `diff-live`. The inspector returns `inspect.ErrIncompleteMetadata` when the visible column count does not match the full `SHOW CREATE TABLE` definition.

## Go API

`migrate.Runner` is available when an application needs to embed migration execution in a separate administrative program. Each `migrate.Statement` holds a source name and native SQL text. Supply the complete migration set to `Runner.Apply`; it orders migrations by ID and rejects duplicate IDs, missing recorded migrations, skipped migrations, and changed source checksums.

The runner does not infer migrations from Go table descriptors, compare live schemas, or repair a database automatically. The PostgreSQL diff command compares checked-in desired-schema sources instead. `inspect` can still compare supported metadata outside the migration runner.
