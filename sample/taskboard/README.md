# Taskboard sample application

This standalone module runs a small SQLite taskboard. Its checked-in [DDL](schema.sql) creates members, projects, tasks, constraints, and an index. The application uses typed descriptors to insert rows, update a task, and stream a joined result.

Run it from this directory:

```sh
go run .
```

Run its integration test with:

```sh
go test ./...
```
