# Taskboard sample application

This standalone module runs a small SQLite Taskboard web application. The schema descriptors in `internal/store` create members, projects, tasks, constraints, and an index through `rasql.Create`. The repository uses the same descriptors to insert rows, update a task, and serve the open tasks page.

- `cmd/taskboard` wires the SQLite database, rasql client, and HTTP server.
- `internal/store` owns schema descriptors and persistence through rasql.
- `internal/taskboard` owns the taskboard view model.
- `internal/web` owns HTTP request handling and server lifecycle.

Run it from this directory:

```sh
go run ./cmd/taskboard
```

Open <http://127.0.0.1:8080/> to see the Taskboard page. Set `TASKBOARD_ADDR` to use another listener address.

Run its integration test with:

```sh
go test ./...
```
