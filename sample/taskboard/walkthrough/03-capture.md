# 3. Capture the migration

Chapter 2 settled the tables by hand. This chapter records those decisions in
the migration tree that the application owns. Every migration has an `up` file
and a matching `down` file; the generator uses only the ordered `up` files.

## Keep the schema in migrations

Create migration directories and put chapter 2's SQL into them:

```text
db/migrations/
  001_initial/
    001_create_members.up.sql
    002_create_projects.up.sql
    003_create_tasks.up.sql
    004_create_index_tasks_open_by_project.up.sql
  002_due_dates_and_unowned_tasks/
    001_add_due_on.up.sql
    002_relax_assignee.up.sql
    003_assignee_on_delete_set_null.up.sql
```

The down files reverse one migration step. They help local development, but
they do not belong in generation input. The explicit list in `rasql.json`
keeps that boundary visible.

Apply the tree to a disposable database:

```sh
./scripts/rasql.sh migrate apply -dir db/migrations \
  -dialect postgresql -dsn "$TASKBOARD_SCHEMA_DSN"
```

The application never creates tables at startup. Deployment runs this command
before the service starts.

## Check the migration

The migration history records each applied directory and its checksum:

```sh
./scripts/rasql.sh migrate status -dir db/migrations \
  -dialect postgresql -dsn "$TASKBOARD_SCHEMA_DSN"
./scripts/rasql.sh migrate verify -dir db/migrations \
  -dialect postgresql -dsn "$TASKBOARD_SCHEMA_DSN"
```

The schema update helper combines the setup operations needed to refresh a
lock. It requires an explicitly named disposable database:

```sh
TASKBOARD_SCHEMA_DSN="$TASKBOARD_DSN" ./scripts/refresh-schema.sh
```

The helper is the only walkthrough command that captures live metadata. The
next chapter shows how ordinary generation and checking run without a DSN.
