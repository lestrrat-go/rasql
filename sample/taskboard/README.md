# Taskboard sample application

This is a standalone module that runs a small SQLite Taskboard web application. Its module path is `example.com/taskboard` rather than a path under `github.com/lestrrat-go/rasql`, so it reaches rasql only through the public API a real project has, and a copy of this directory is a working starting point.

- `cmd/taskboard` opens the SQLite database, then wires the rasql db and HTTP server.
- `migrations` holds the ordered schema changes.
- `gen` holds the generator program this project owns.
- `scripts` wraps every call to the `rasql` command, so a step is run rather than retyped.
- `internal/store` holds the generated store and the persistence code built on it.
- `internal/taskboard` owns the taskboard view model.
- `internal/web` owns HTTP request handling and server lifecycle.

Create the runtime database and start the application from this directory:

```sh
./scripts/migrate.sh
TASKBOARD_DSN=taskboard.db go run ./cmd/taskboard
```

`scripts/migrate.sh` applies `migrations/sqlite` with `rasql migrate apply`, against `taskboard.db` or whatever `TASKBOARD_DSN` names.

Regenerate the checked-in store after adding a migration:

```sh
./scripts/generate.sh
```

`scripts/generate.sh` rebuilds a throwaway schema database from the same migrations, then runs `gen`, which inspects that database and writes one `<table>_gen.go` file per table. The throwaway database is `internal/store/.taskboard-schema.db` and is ignored by Git. `go generate ./...` runs the same script through the directive in `gen/main.go`.

Report whether the checked-in store is current without writing files:

```sh
./scripts/generate.sh -check
```

Open <http://127.0.0.1:8080/> to see the Taskboard page. Set `TASKBOARD_ADDR` to use another listener address. Set `TASKBOARD_DSN` to use a different SQLite database path.

`GET /healthz` is a liveness probe reporting process health. It always returns `200 ok` without querying the database, so a store outage does not restart or kill an otherwise healthy process.

Run its integration test with:

```sh
go test ./...
```
