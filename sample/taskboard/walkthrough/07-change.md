# 7. Change the schema safely

The migration tree is the durable schema contract. A change adds a new ordered
migration and leaves old files untouched. This chapter follows the sequence
used for the due date and nullable assignee changes.

## Add a migration

Write the next `up.sql` and `down.sql` pair under a new migration ID. Apply
it to a disposable database and verify the history:

```sh
./scripts/rasql.sh migrate apply -dir db/migrations \
  -dialect postgresql -dsn "$TASKBOARD_SCHEMA_DSN"
./scripts/rasql.sh migrate verify -dir db/migrations \
  -dialect postgresql -dsn "$TASKBOARD_SCHEMA_DSN"
```

The explicit `schema.paths` list in `rasql.json` must receive the new up
file at its terminal position. Down files stay out of that list.

## Refresh the snapshot

The refresh is an owned, engine-backed operation. It applies the migration tree
and updates the lock from the disposable database:

```sh
TASKBOARD_SCHEMA_DSN="$TASKBOARD_DSN" ./scripts/refresh-schema.sh
```

Review the lock and generated diff. Then run generation with every DSN variable
removed:

```sh
env -u TASKBOARD_SCHEMA_DSN -u TASKBOARD_DSN -u TASKBOARD_TEST_DSN \
  ./scripts/generate.sh
env -u TASKBOARD_SCHEMA_DSN -u TASKBOARD_DSN -u TASKBOARD_TEST_DSN \
  ./scripts/rasql.sh check
```

The application code changes only after the generated package has the needed
typed symbols. A nullable database column is represented in rows by
`rasql.Nullable` and in the graph by `LoadedOne`; the repository preserves
both distinctions.

## Keep reads stable

The root page remains keyed by project ID. Child tasks retain their ID order
and per-project limit. A schema change can add a field to generated whole-row
projections without changing the page's root query or cursor contract.
