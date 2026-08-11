# Taskboard sample application

This standalone module runs a small SQLite Taskboard web application. The application only opens and queries its database. Run its checked-in SQLite SQL migrations with `rasqlmigrate` before starting it.

- `cmd/taskboard` opens the SQLite database, then wires the rasql db and HTTP server.
- `migrations` holds the ordered schema changes applied before the application starts.
- `internal/store` owns schema descriptors and persistence through rasql.
- `internal/taskboard` owns the taskboard view model.
- `internal/web` owns HTTP request handling and server lifecycle.

Run it from this directory:

```sh
go run ../../cmd/rasqlmigrate plan \
  -dir migrations/sqlite

go run ../../cmd/rasqlmigrate apply \
  -dir migrations/sqlite \
  -dialect sqlite \
  -dsn taskboard.db

TASKBOARD_DSN=taskboard.db go run ./cmd/taskboard
```

Regenerate the checked-in store descriptors after adding a migration:

```sh
go generate ./internal/store
```

This creates `internal/store/.taskboard-schema.db`, applies the SQLite migrations with `rasqlmigrate`, and generates one `<table>_gen.go` file per table with `rasqlgen -dsn`.

Open <http://127.0.0.1:8080/> to see the Taskboard page. Set `TASKBOARD_ADDR` to use another listener address. Set `TASKBOARD_DSN` to use a different SQLite database path.

`GET /healthz` is a liveness probe reporting process health. It always returns `200 ok` without querying the database, so a store outage does not restart or kill an otherwise healthy process.

Run its integration test with:

```sh
go test ./...
```
