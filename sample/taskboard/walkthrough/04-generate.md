# 4. Lock the schema and generate offline

The database is the source used to make a snapshot. The snapshot is the source
used for normal builds. A clean checkout can therefore generate the same Go
files without credentials or a running database.

## Configure the inputs

Create `rasql.json` at the project root. It names the PostgreSQL profile, the
ordered migration inputs chapter 3 just applied, and the compact store
package:

```json
{
  "engine": {"dialect": "postgresql", "profile": "postgresql-17"},
  "schema": {
    "kind": "migrations",
    "identity": "taskboard-migrations-v1",
    "paths": [
      "db/migrations/001_initial/001_create_members.up.sql",
      "db/migrations/001_initial/002_create_projects.up.sql",
      "db/migrations/001_initial/003_create_tasks.up.sql",
      "db/migrations/001_initial/004_create_index_tasks_open_by_project.up.sql"
    ]
  },
  "package": "store",
  "output": "internal/store",
  "emitter": "compact"
}
```

The `schema.paths` list contains only `up.sql` files, in application order.
[Chapter 7](07-change.md) appends to this list rather than editing an entry,
so every past migration keeps generating the schema it always generated.

## Add a schema refresh helper

Capturing the schema needs its own disposable database, separate from the
one `TASKBOARD_DSN` names, because the capture step is free to apply
migrations to it without touching the database the running application uses.
Wrap that in a script:

Create `scripts/refresh-schema.sh`:

```sh
#!/bin/sh
set -eu
dsn="${TASKBOARD_SCHEMA_DSN:?set TASKBOARD_SCHEMA_DSN to a disposable PostgreSQL database}"
rasql migrate apply -dir db/migrations -dialect postgresql -dsn "$dsn"
exec env TASKBOARD_SCHEMA_DSN="$dsn" rasql schema update --dsn "$dsn"
```

```sh
chmod +x scripts/refresh-schema.sh
git add rasql.json scripts/refresh-schema.sh
git commit -m 'lock the schema from a disposable database'
```

## Capture once

Point `TASKBOARD_SCHEMA_DSN` at a database made for this capture alone, apply
the migrations to it, and let `rasql schema update` write the lock:

```sh
export TASKBOARD_SCHEMA_DSN='postgres://rasql:rasql@127.0.0.1:5432/taskboard_schema?sslmode=disable'
./scripts/refresh-schema.sh
```

```text
applied	001_initial
migration apply completed: 1 applied
updated schema and generated internal/store
```

That command writes `rasql.lock.json` and the compact generated files. It
does not alter migration or query text, and it is the only command in this
chapter that touches a database.

## Generate and check without a database

After capture, generation reads only checked-in inputs. Create
`scripts/generate.sh`:

```sh
#!/bin/sh
# Rebuild internal/store from checked-in snapshots and query inputs.
set -eu
unset TASKBOARD_SCHEMA_DSN TASKBOARD_DSN TASKBOARD_TEST_DSN
exec rasql generate "$@"
```

```sh
chmod +x scripts/generate.sh
```

```sh
env -u TASKBOARD_SCHEMA_DSN -u TASKBOARD_DSN -u TASKBOARD_TEST_DSN \
  ./scripts/generate.sh
env -u TASKBOARD_SCHEMA_DSN -u TASKBOARD_DSN -u TASKBOARD_TEST_DSN \
  ./scripts/generate.sh -check
```

```text
generated internal/store offline
internal/store is up to date
```

The first command rewrites generated files. The second, `-check`, reports
stale output without writing anything.

## What the generator printed

Chapter 1 promised two consequences of version one's design: every column is
required, so every generated field is a plain Go value, and both foreign keys
sit on required columns, so both get a relationship accessor. The generated
row types show the first of those:

```go
type MembersRow struct {
	ID   int64
	Name string
}
```

```go
type TasksRow struct {
	ID, ProjectID, AssigneeID int64
	Title                     string
	IsOpen                    bool
	CreatedAt                 time.Time
}
```

Every field is a plain value. Nothing here is a pointer, because chapter 2
made every column required. [Chapter 7](07-change.md) relaxes `assignee_id`
and this same struct grows a pointer in exactly that field, and nowhere else.

The generated package also contains aliased typed relations, whole-row
projections, and fluent create and patch inputs for each table. [Chapter
5](05-queries.md) builds the repository on top of these symbols instead of
rebuilding their metadata.

Commit the generated store:

```sh
git add rasql.lock.json internal/store
git commit -m 'generate the store from the schema'
```

## Next

[Build the typed repository](05-queries.md).
