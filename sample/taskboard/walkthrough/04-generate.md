# 4. Generate the store from the database

Applying `db/migrations` to the database `TASKBOARD_DSN` names, then reading
its catalog back, is what produces the store. A clean checkout can prove the
checked-in files still match their inputs without a database, and can
regenerate them only with one.

## Configure the inputs

Create `rasql.json` at the project root. It names the PostgreSQL dialect, the
migration directory chapter 3 applied, and the compact store package:

```json
{
  "dialect": "postgresql",
  "migrations": "db/migrations",
  "package": "store",
  "output": "internal/store",
  "emitter": "compact"
}
```

## Add a generate helper

Generating needs the migrations applied to the database `TASKBOARD_DSN`
names first, so one script does both:

Create `scripts/generate.sh`:

```sh
#!/bin/sh
# Rebuild internal/store from the database TASKBOARD_DSN names, after
# applying db/migrations to it.
set -eu
dsn="${TASKBOARD_DSN:?set TASKBOARD_DSN to the taskboard connection string}"
./scripts/migrate.sh apply
exec rasql codegen generate -dsn "$dsn" "$@"
```

```sh
chmod +x scripts/generate.sh
git add rasql.json scripts/generate.sh
git commit -m 'configure generation from the database'
```

## Generate

```sh
./scripts/generate.sh
```

```text
migration apply completed: 0 applied
generated internal/store
```

Chapter 3 already applied `001_initial`, so this run finds nothing pending
and generates straight away. Running `./scripts/generate.sh` again later,
after a migration that is still pending, applies it first; that is chapter
7.

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
git add internal/store
git commit -m 'generate the store from the database'
```

## Check without a database

`internal/store` carries `rasql.sum` beside the generated Go, recording the
dialect, the profile, the migration checksums, the query inputs, and a hash
of every generated file. `rasql codegen check` recomputes all of that from
the working tree alone and reports whether it still matches, without opening
a connection:

```sh
env -u TASKBOARD_DSN -u TASKBOARD_TEST_DSN rasql codegen check
```

```text
internal/store is up to date; no database was consulted
```

That is the gate a clean checkout runs: it proves the checked-in files match
their inputs, and it needs no credentials and no running server to do it.
Only `./scripts/generate.sh`, which does need both, can bring `internal/store`
up to date with a schema or query change.

## Next

[Build the typed repository](05-queries.md).
