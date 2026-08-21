# 3. Capture the schema into a migration

[Chapter 2](02-database.md) left two things behind. The working database carries the three tables, shaped by hand and argued for one decision at a time. `db/shape.sql` carries the statements that built them.

That file is not yet a schema anyone can rely on. It works only against an empty database, so it cannot be re-run. Nothing anywhere records whether it has been run, so a second developer cloning the project has no way to find out what state their database is in. Chapter 2 ended by naming the SQL as the schema of record, and this chapter turns that SQL into a migration `rasql migrate` can apply, re-apply safely, report on, and undo.

## Get the rasql command

Everything from here on runs through the `rasql` command:

```sh
go install github.com/lestrrat-go/rasql/cmd/rasql@latest
```

That line was not run for this walkthrough, and running it today would install a `rasql` without the command this chapter is about. `rasql migrate dump` is newer than the latest tagged release, so every `rasql` call below was made against a binary built from a checkout of the repository with `go build ./cmd/rasql`. Once `dump` reaches a release, the line above is all it takes.

## What `rasql migrate dump` reads and writes

`rasql migrate dump` connects to a database, inspects every base table it finds inside one read-only transaction, and prints the DDL that rebuilds them. It writes two shapes, and [Dump a live schema](../../../docs/core/07-migrations.md#dump-a-live-schema) is the reference for every flag it takes.

`-format schema`, the default, writes one `<table>.sql` per table holding that table's `CREATE TABLE` and any `CREATE INDEX` beside it. That is the directory layout `rasql migrate diff` reads as `-from` and `-to`, which is how a later change is turned into a migration.

`-format migration` writes a migration directory: an `.up.sql` and a `.down.sql` per table, plus an `.up.sql` per index, numbered in foreign-key dependency order so the tables are created before the tables that reference them.

Omitting `-output` prints the whole dump to stdout instead of writing files, which is how to look before committing to anything. Point it at the working database and ask for the migration shape:

```sh
rasql migrate dump \
  -dialect postgresql \
  -dsn "$TASKBOARD_DSN" \
  -format migration
```

```text
-- 001_create_members.up.sql
CREATE TABLE "members" ("id" BIGINT NOT NULL GENERATED ALWAYS AS IDENTITY, "name" TEXT NOT NULL, PRIMARY KEY ("id"));

-- 001_create_members.down.sql
DROP TABLE "members";

-- 002_create_projects.up.sql
CREATE TABLE "projects" ("id" BIGINT NOT NULL GENERATED ALWAYS AS IDENTITY, "name" TEXT NOT NULL, PRIMARY KEY ("id"));

-- 002_create_projects.down.sql
DROP TABLE "projects";

-- 003_create_tasks.up.sql
CREATE TABLE "tasks" ("id" BIGINT NOT NULL GENERATED ALWAYS AS IDENTITY, "project_id" BIGINT NOT NULL, "assignee_id" BIGINT NOT NULL, "title" TEXT NOT NULL, "is_open" BOOLEAN NOT NULL DEFAULT true, "created_at" TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY ("id"), CONSTRAINT "tasks_assignee_id_fkey" FOREIGN KEY ("assignee_id") REFERENCES "members" ("id") ON DELETE NO ACTION ON UPDATE NO ACTION, CONSTRAINT "tasks_project_id_fkey" FOREIGN KEY ("project_id") REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION);

-- 003_create_tasks.down.sql
DROP TABLE "tasks";

-- 004_create_index_tasks_open_by_project.up.sql
CREATE INDEX "tasks_open_by_project" ON "tasks" ("project_id", "id") WHERE is_open;
```

Every decision chapter 2 argued for is in there. `GENERATED ALWAYS AS IDENTITY` survived, `BIGINT` and `TIMESTAMPTZ` are the types the database has, `ON DELETE CASCADE` and `ON DELETE NO ACTION` are each on the key that earned them, `DEFAULT true` and `DEFAULT now()` are intact, and the partial index kept its `WHERE is_open`.

The text is not what `db/shape.sql` says. Identifiers are quoted, type names are upper case, and each table is one long line rather than one column per line. It is the same schema stated in rasql's own words, which is what matters: chapter 2's version was typed by a person, and this one was read back out of the database that person built.

## Write the migration

`-output` names the migration directory to create. `rasql migrate apply` takes the migration *root*, whose first-level entries are the migration IDs, so the directory to name here is one level inside that root:

```sh
rasql migrate dump \
  -dialect postgresql \
  -dsn "$TASKBOARD_DSN" \
  -format migration \
  -output db/migrations/001_initial
```

```text
created db/migrations/001_initial
```

```text
db/migrations/
  001_initial/
    001_create_members.up.sql
    001_create_members.down.sql
    002_create_projects.up.sql
    002_create_projects.down.sql
    003_create_tasks.up.sql
    003_create_tasks.down.sql
    004_create_index_tasks_open_by_project.up.sql
```

Seven files for four steps forward. The index has no reverse source of its own, because dropping `tasks` drops the index with it, and [Migrations](../../../docs/core/07-migrations.md#the-rules-a-migration-root-follows) allows a migration to hold fewer reverse sources than forward ones for exactly that reason.

Running the same command a second time changes nothing:

```text
dump: output directory "db/migrations/001_initial" is not empty
```

There is no flag that overrides that. A captured migration is meant to be reviewed and committed once, and a command that could silently rewrite one already applied elsewhere would be a way to break every database that ran it.

## Retire `db/shape.sql`

`db/migrations/001_initial` now says everything `db/shape.sql` said, and it says it in a form that can be applied, checked, and reverted. Keeping both would leave two files claiming to describe the schema with nothing keeping them in step, which is the problem chapter 2 spent its last section arguing against. Delete it in the same commit that adds the migration, so no revision of the project ever holds both:

```sh
git rm db/shape.sql
git add db/migrations
git commit -m 'capture the shaped schema into a migration'
```

## Give the migrate calls a shorter name

`rasql migrate` takes the same three flags on every subcommand. `scripts/psql.sh` already saved the chapter from retyping a `podman exec` line; do the same here, and pass the subcommand through so one script serves `apply`, `status`, `verify`, and `revert` alike. Create `scripts/migrate.sh`:

```sh
#!/bin/sh
# Run one rasql migrate subcommand against the database TASKBOARD_DSN names:
#
#   ./scripts/migrate.sh apply
#   ./scripts/migrate.sh status
#
# Every argument after the subcommand is passed through, so a run can add
# -dry-run, -steps, or anything else the subcommand takes.
set -eu
subcommand="${1:?name a rasql migrate subcommand, such as apply or status}"
shift
exec rasql migrate "$subcommand" \
	-dir db/migrations \
	-dialect postgresql \
	-dsn "${TASKBOARD_DSN:?set TASKBOARD_DSN to the taskboard connection string}" \
	"$@"
```

```sh
chmod +x scripts/migrate.sh
git add scripts/migrate.sh
git commit -m 'add a migrate helper'
```

## Prove the migration rebuilds what was shaped

A captured migration is only worth anything if applying it produces the schema it was captured from. Test that against a database that has never been touched by hand. Create an empty one:

```sh
podman exec rasql-postgres psql -U rasql -d postgres -c 'CREATE DATABASE taskboard_verify;'
```

Apply the migration to it:

```sh
TASKBOARD_DSN='postgres://rasql:rasql@127.0.0.1:5432/taskboard_verify?sslmode=disable' \
  ./scripts/migrate.sh apply
```

```text
applied	001_initial
migration apply completed: 1 applied
```

Ask what it built:

```sh
podman exec rasql-postgres psql -U rasql -d taskboard_verify -c '\d tasks'
```

```text
                                     Table "public.tasks"
   Column    |           Type           | Collation | Nullable |           Default
-------------+--------------------------+-----------+----------+------------------------------
 id          | bigint                   |           | not null | generated always as identity
 project_id  | bigint                   |           | not null |
 assignee_id | bigint                   |           | not null |
 title       | text                     |           | not null |
 is_open     | boolean                  |           | not null | true
 created_at  | timestamp with time zone |           | not null | now()
Indexes:
    "tasks_pkey" PRIMARY KEY, btree (id)
    "tasks_open_by_project" btree (project_id, id) WHERE is_open
Foreign-key constraints:
    "tasks_assignee_id_fkey" FOREIGN KEY (assignee_id) REFERENCES members(id)
    "tasks_project_id_fkey" FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
```

That is the table chapter 2 ended with, column for column. Reading two `\d` outputs side by side is a poor way to be sure of that, though, so let the command that captured the schema compare them instead. Dump both databases and diff the two dumps:

```sh
rasql migrate dump -dialect postgresql \
  -dsn 'postgres://rasql:rasql@127.0.0.1:5432/taskboard_walkthrough?sslmode=disable' > shaped.sql
rasql migrate dump -dialect postgresql \
  -dsn 'postgres://rasql:rasql@127.0.0.1:5432/taskboard_verify?sslmode=disable' > applied.sql
diff shaped.sql applied.sql
```

`diff` printed nothing and exited 0. The database built by hand in chapter 2 and the database built by the migration describe the same schema, read back through the same inspector.

The dump skips the history table `rasql migrate apply` created, so its presence in the verified database is not what makes the two agree. `-history-table` names that table when an application has renamed it.

`taskboard_verify` stays. [Chapter 4](04-generate.md) has a use for a database whose schema came from the migrations rather than from a person.

## What the command refuses to capture

`rasql migrate dump` writes DDL by rendering the descriptor rasql's inspection returns. Chapter 2 found three PostgreSQL types that do not survive that trip: `integer` and `smallint` come back as `BIGINT`, and `timestamp` without a zone comes back as `TIMESTAMPTZ`. Rather than emit a schema that quietly differs from the one it read, the command refuses the table.

A throwaway database with a `date` column shows what that looks like:

```sql
CREATE TABLE reports (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  seen_on date NOT NULL,
  score integer NOT NULL,
  noted_at timestamp NOT NULL
);
```

```text
dump: table "reports" column "seen_on" is declared "date", which rasql renders as "TIMESTAMPTZ"; capturing it would build a different column, so this table cannot be dumped
```

Dropping that column moves the complaint to the next one:

```text
dump: table "reports" column "score" is declared "integer", which rasql renders as "BIGINT"; capturing it would build a different column, so this table cannot be dumped
```

```text
dump: table "reports" column "noted_at" is declared "timestamp without time zone", which rasql renders as "TIMESTAMPTZ"; capturing it would build a different column, so this table cannot be dumped
```

`date`, `real`, and `json` are refused on the same grounds. [Dump a live schema](../../../docs/core/07-migrations.md#dump-a-live-schema) lists the types each dialect does allow. A schema that uses any of them cannot be captured by this command today; its migrations have to be written by hand. Taskboard passes because chapter 2 chose `bigint` and `timestamptz` for reasons of its own, long before this command entered the story, and those reasons happen to be the same ones: a type that means something different after a round trip is a type the application should not have picked.

## Next

[Generate the store](04-generate.md).
