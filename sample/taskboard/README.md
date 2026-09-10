# Taskboard

A team runs projects, each project holds tasks, and a task is owned by one
member of the team or by nobody. One HTML page lists the open tasks grouped by
project, prints each task's owner and due date, counts the tasks that are past
that date, offers a form to add a task, and offers a button to close one.

The database is PostgreSQL. The schema lives in `db/migrations` and is applied
by `rasql migrate apply`. `rasql codegen generate -dsn` then reads that
database and writes `internal/store` and `rasql.sum`; `rasql codegen check`
proves the checked-in files still match their inputs, with a database or
without one.

Taskboard is a module of its own, `example.com/taskboard`, so it reaches
[rasql](https://github.com/lestrrat-go/rasql) only through the public API a
real project has.

## How this project was built

[The walkthrough](walkthrough/01-design.md) is nine chapters long and builds
this application from an empty directory: it settles the schema against a
running PostgreSQL server, captures it into a migration, generates the store,
writes the repository, draws the page, changes the schema and follows the
compiler through the fallout, and ends on the tests and the migration commands.
Every command it shows was run, and the code below is what running them
produced.

## What this copy spells differently

The walkthrough builds Taskboard as a standalone project beside a rasql
checkout, so its `go.mod` redirects the dependency one directory up:

```
replace github.com/lestrrat-go/rasql => ../rasql
```

This copy is checked into the rasql repository itself, two directories below
its root, so its `go.mod` redirects the dependency there instead:

```
replace github.com/lestrrat-go/rasql => ../..
```

The scripts differ for the same reason: they run the `rasql` command out of
that checkout rather than one `go install` put on the PATH. `scripts/rasql.sh`
is a whole file the walkthrough never shows: it builds `../../cmd/rasql` and
runs the result. `scripts/generate.sh` and `scripts/migrate.sh` each add a
line to run from the module root and call `scripts/rasql.sh` instead of
naming `rasql` directly.

`internal/store/docs_examples_test.go` is a compile-only source for short API
snippets in rasql's reference documentation. It calls the same generated store
and canonical runtime APIs as the repository, but it is not part of the
walkthrough steps or the application binary.

## What is in here

- `db/migrations` holds the schema, one directory per migration.
- `queries` holds the SQL templates `rasql.json` compiles into Go functions.
- `internal/store` holds the generated store, graph repository, and typed SQL.
- `internal/taskboard` holds the view model the page is drawn from.
- `internal/web` holds the handler and the page template.
- `cmd/taskboard` opens the database and runs the server.
- `rasql.json` holds the dialect, the migration directory, and the typed query.
- `scripts` wraps the `rasql` calls, so a step is run rather than retyped.
- `walkthrough` is the nine chapters that produced all of the above, and
  `walkthrough/steps.bundle` is the repository they were followed in, one commit
  per step, in a single file. `git clone` expands it.

## What running it needs

A PostgreSQL server, and a database on it for this application. The walkthrough
runs one under podman:

```sh
podman run -d --name rasql-postgres \
  -e POSTGRES_USER=rasql \
  -e POSTGRES_PASSWORD=rasql \
  -e POSTGRES_DB=rasql \
  -p 5432:5432 \
  docker.io/library/postgres:17-alpine
```

```sh
podman exec rasql-postgres psql -U rasql -d postgres -c 'CREATE DATABASE taskboard;'
```

`scripts/psql.sh` opens `psql` on the database `TASKBOARD_DATABASE` names,
defaulting to `rasql_taskboard` rather than the database just created,
and takes `TASKBOARD_CONTAINER` to reach another container. Set
`TASKBOARD_DATABASE` to `taskboard` to reach this one.

## Run it

Apply the schema, then start the server:

```sh
export TASKBOARD_DSN='postgres://rasql:rasql@127.0.0.1:5432/taskboard?sslmode=disable'
export TASKBOARD_DATABASE=taskboard
./scripts/migrate.sh apply
go run ./cmd/taskboard
```

Open <http://127.0.0.1:8080/> to see the page. `TASKBOARD_ADDR` listens
somewhere else.

The page files tasks against a project and an owner that already exist, and
writes no rows of its own at startup, so put a project and a member in before
the form has anything to offer:

```sh
./scripts/psql.sh -c "
INSERT INTO members (name) VALUES ('Ada Lovelace'), ('Grace Hopper');
INSERT INTO projects (name) VALUES ('Website refresh'), ('Billing cleanup');"
```

## Regenerate the store

`internal/store`'s generated files and `rasql.sum` are checked in. Applying
`db/migrations` to the database `TASKBOARD_DSN` names and reading it back is
what regenerates them:

```sh
./scripts/generate.sh
```

`./scripts/rasql.sh codegen check` proves the checked-in files match their
inputs without opening a database. `./scripts/rasql.sh codegen check -dsn
"$TASKBOARD_DSN"` proves they match the database too.

## Run the tests

Everything but `internal/store` runs without a database:

```sh
go test ./...
```

`internal/store`'s tests need a migrated PostgreSQL database and skip
themselves without one. They run inside a transaction that is rolled back, so
they leave nothing behind:

```sh
TASKBOARD_TEST_DSN="$TASKBOARD_DSN" go test ./...
```
