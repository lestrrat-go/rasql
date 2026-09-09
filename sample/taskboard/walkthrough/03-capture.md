# 3. Capture the migration

Chapter 2 settled the tables by hand and then dropped them again, leaving
`db/shape.sql` as a written record of the decision and an empty database.
This chapter records that same shape in a migration tree the application
owns, and applies it for real for the first time.

## Build rasql from a checkout

Chapter 2 deferred one thing: what actually ran in place of
`go install github.com/lestrrat-go/rasql/cmd/rasql@latest`. This walkthrough
was written against the checkout cloned into `../rasql`, ahead of a release,
so the command was built from that checkout instead of installed from a tag:

```sh
go build -o "$(go env GOPATH)/bin/rasql" ../rasql/cmd/rasql
```

Confirm it is the command every later chapter means by `rasql`:

```sh
rasql -h
```

```text
Usage: rasql <context> <command> [flags]

Contexts:
  schema    Update, import, or verify the declared schema
  generate  Generate from the checked-in schema lock
  check     Check generated output without writing
  codegen   Scaffold the generator program that writes Go source
  migrate   Create and apply versioned SQL migrations

Run 'rasql <context> -h' for context commands.
```

Every `rasql` call from here on refers to this binary.

## Keep the schema in migrations

Create one migration directory, `001_initial`, and give it one file per
statement in chapter 2's `db/shape.sql`, in the order the tables and index
have to exist for each other:

```text
db/migrations/001_initial/
  001_create_members.up.sql
  001_create_members.down.sql
  002_create_projects.up.sql
  002_create_projects.down.sql
  003_create_tasks.up.sql
  003_create_tasks.down.sql
  004_create_index_tasks_open_by_project.up.sql
```

Every identifier is quoted, so a future rename or a case-sensitive name
never depends on how PostgreSQL folds an unquoted one:

```sql
-- 001_create_members.up.sql
CREATE TABLE "members" ("id" BIGINT NOT NULL GENERATED ALWAYS AS IDENTITY, "name" TEXT NOT NULL, PRIMARY KEY ("id"));
```

```sql
-- 001_create_members.down.sql
DROP TABLE "members";
```

```sql
-- 002_create_projects.up.sql
CREATE TABLE "projects" ("id" BIGINT NOT NULL GENERATED ALWAYS AS IDENTITY, "name" TEXT NOT NULL, PRIMARY KEY ("id"));
```

```sql
-- 002_create_projects.down.sql
DROP TABLE "projects";
```

```sql
-- 003_create_tasks.up.sql
CREATE TABLE "tasks" ("id" BIGINT NOT NULL GENERATED ALWAYS AS IDENTITY, "project_id" BIGINT NOT NULL, "assignee_id" BIGINT NOT NULL, "title" TEXT NOT NULL, "is_open" BOOLEAN NOT NULL DEFAULT true, "created_at" TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY ("id"), CONSTRAINT "tasks_assignee_id_fkey" FOREIGN KEY ("assignee_id") REFERENCES "members" ("id") ON DELETE NO ACTION ON UPDATE NO ACTION, CONSTRAINT "tasks_project_id_fkey" FOREIGN KEY ("project_id") REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION);
```

```sql
-- 003_create_tasks.down.sql
DROP TABLE "tasks";
```

```sql
-- 004_create_index_tasks_open_by_project.up.sql
CREATE INDEX "tasks_open_by_project" ON "tasks" ("project_id", "id") WHERE is_open;
```

A partial index has no reverse statement of its own; dropping `tasks` in the
matching down migration takes it with the table. Down files reverse one
migration step and help local development, but they never feed generation:
chapter 4's `rasql.json` names only `up.sql` files, in application order.

## Add a migrate helper

Every later chapter runs `rasql migrate` against the database `TASKBOARD_DSN`
names, with the same `-dir`, `-dialect`, and `-dsn` flags. Wrap that once:

```sh
mkdir -p scripts
```

Create `scripts/migrate.sh`:

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

Make it executable and commit the migration tree alongside it:

```sh
chmod +x scripts/migrate.sh
git add db/migrations scripts/migrate.sh
git commit -m 'capture the shaped schema into a migration'
```

## Apply the tree

`TASKBOARD_DSN` still names the database chapter 2 emptied, so this is the
first time anything creates these tables through a tracked migration:

```sh
./scripts/migrate.sh apply
```

```text
applied	001_initial
migration apply completed: 1 applied
```

The application never creates tables at startup. Deployment runs this
command before the service starts.

## Check the migration

The migration history records each applied directory and its checksum:

```sh
./scripts/migrate.sh status
```

```text
applied	001_initial
```

```sh
./scripts/migrate.sh verify
```

```text
migration verification passed
```

The schema now exists twice: once as the durable migration tree just applied,
and once, read back live, as the input [chapter 4](04-generate.md) locks into
a snapshot and generates Go from.

## Next

[Lock the schema and generate offline](04-generate.md).
