# Taskboard sample application

This standalone module runs a small SQLite taskboard. The schema descriptors in `internal/store` create members, projects, tasks, constraints, and an index through `rasql.Create`. The repository uses the same descriptors to insert rows, update a task, and stream a joined result.

- `cmd/taskboard` wires the SQLite database, rasql client, and application.
- `internal/store` owns schema descriptors and persistence through rasql.
- `internal/taskboard` owns the taskboard workflow and view.

Run it from this directory:

```sh
go run ./cmd/taskboard
```

Run its integration test with:

```sh
go test ./...
```
