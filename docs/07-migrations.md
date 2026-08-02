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

## Go API

`migrate.Runner` is available when an application needs to embed migration execution in a separate administrative program. Each `migrate.Statement` holds a source name and native SQL text. Supply the complete migration set to `Runner.Apply`; it orders migrations by ID and rejects duplicate IDs, missing recorded migrations, skipped migrations, and changed source checksums.

The runner does not infer migrations from Go table descriptors, compare live schemas, or repair a database automatically. `inspect` can still compare supported metadata outside the migration runner.
