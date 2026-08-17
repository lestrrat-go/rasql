# Taskboard sample application

This standalone module runs a small SQLite Taskboard web application. The application only opens and queries its database. The sample already includes the `gen/main.go` scaffold that `rasqlgen init` creates for a new project. Run its checked-in SQLite SQL migrations with `rasqlmigrate` before starting it.

- `cmd/taskboard` opens the SQLite database, then wires the rasql db and HTTP server.
- `migrations` holds the ordered schema changes applied before the application starts.
- `internal/store` owns schema descriptors and persistence through rasql.
- `internal/taskboard` owns the taskboard view model.
- `internal/web` owns HTTP request handling and server lifecycle.

Initialize the runtime database from this directory:

```sh
go run ../../cmd/rasqlmigrate plan \
  -dir migrations/sqlite

go run ../../cmd/rasqlmigrate apply \
  -dir migrations/sqlite \
  -dialect sqlite \
  -dsn taskboard.db

TASKBOARD_DSN=taskboard.db go run ./cmd/taskboard
```

Regenerate the checked-in store descriptors after adding a migration. The sample's checked-in `gen/main.go` owns this workflow; `rasqlgen init` is only needed to create that file in a new project:

```sh
go generate ./...
```
<!-- This recursive command is valid because the generate directive is in gen/main.go; internal/store contains generated output and has no directive. -->

Check the checked-in store without writing files:

```sh
go run ./gen -check
```

The checked-in `gen/main.go` creates `internal/store/.taskboard-schema.db`, loads and applies the SQLite migrations, inspects the live schema, and generates one `<table>_gen.go` file per table. The database file is temporary and ignored by Git.

Open <http://127.0.0.1:8080/> to see the Taskboard page. Set `TASKBOARD_ADDR` to use another listener address. Set `TASKBOARD_DSN` to use a different SQLite database path.

`GET /healthz` is a liveness probe reporting process health. It always returns `200 ok` without querying the database, so a store outage does not restart or kill an otherwise healthy process.

Run its integration test with:

```sh
go test ./...
```
