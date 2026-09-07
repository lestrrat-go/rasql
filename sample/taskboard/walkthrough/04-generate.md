# 4. Lock the schema and generate offline

The database is the source used to make a snapshot. The snapshot is the source
used for normal builds. A clean checkout can therefore generate the same Go
files without credentials or a running database.

## Configure the inputs

`rasql.json` names the PostgreSQL profile, ordered migration inputs, compact
store package, and typed overdue query. The query has one named `on` parameter
and one result, so its generated function exposes those facts in Go. The
migration list contains only `up.sql` files in application order.

`queries/overdue_count.sql` keeps the date boundary in SQL:

```sql
SELECT count(*)::integer AS overdue
FROM tasks
WHERE is_open
  AND due_on IS NOT NULL
  AND due_on < CAST(:on AS date)
```

The cast makes a task due on the caller's calendar day remain current until
that day ends. The caller supplies the location-aware `time.Time`.

## Capture once

Create a disposable schema database, apply the migrations, and capture its
metadata:

```sh
export TASKBOARD_SCHEMA_DSN='postgres://rasql:rasql@127.0.0.1:5432/taskboard_d3_schema?sslmode=disable'
./scripts/refresh-schema.sh
```

That command writes `rasql.lock.json` and compact generated files. It does
not alter migration or query text.

## Generate and check without a database

After capture, generation reads only checked-in inputs:

```sh
env -u TASKBOARD_SCHEMA_DSN -u TASKBOARD_DSN -u TASKBOARD_TEST_DSN \
  ./scripts/generate.sh
env -u TASKBOARD_SCHEMA_DSN -u TASKBOARD_DSN -u TASKBOARD_TEST_DSN \
  ./scripts/rasql.sh check
```

The first command rewrites generated files. The second reports stale output
without writing anything.

The generated package contains row types, aliased typed relations, whole-row
projections, graph edge factories, page-key factories, fluent create and patch
inputs, and the typed `OverdueCount` result. Application code uses these
symbols instead of rebuilding their metadata.
