# 7. Change the schema safely

The migration tree is the durable schema contract. A change adds a new ordered
migration and leaves old files untouched. This chapter follows the sequence
used for the due date and nullable assignee changes.

## Add a migration

Write the next `up.sql` and `down.sql` pair under a new migration ID:

```sql
-- 001_add_due_on.up.sql
ALTER TABLE "tasks" ADD COLUMN "due_on" date;
```

```sql
-- 001_add_due_on.down.sql
ALTER TABLE "tasks" DROP COLUMN "due_on";
```

```sql
-- 002_relax_assignee.up.sql
ALTER TABLE "tasks" ALTER COLUMN "assignee_id" DROP NOT NULL;
```

```sql
-- 002_relax_assignee.down.sql
ALTER TABLE "tasks" ALTER COLUMN "assignee_id" SET NOT NULL;
```

```sql
-- 003_assignee_on_delete_set_null.up.sql
ALTER TABLE "tasks"
  DROP CONSTRAINT "tasks_assignee_id_fkey",
  ADD CONSTRAINT "tasks_assignee_id_fkey"
    FOREIGN KEY ("assignee_id") REFERENCES "members" ("id") ON DELETE SET NULL;
```

```sql
-- 003_assignee_on_delete_set_null.down.sql
ALTER TABLE "tasks"
  DROP CONSTRAINT "tasks_assignee_id_fkey",
  ADD CONSTRAINT "tasks_assignee_id_fkey"
    FOREIGN KEY ("assignee_id") REFERENCES "members" ("id") ON DELETE NO ACTION;
```

Unlike `001_initial`, this migration can be undone: each `.down.sql` reverses
exactly the statement its `.up.sql` made. `db/migrations` needs no other
change: `rasql.json` names the whole directory, so a new migration under it
is picked up without editing the file.

Applying the migration and regenerating are the same script that chapter 4
introduced:

```sh
./scripts/generate.sh
```

```text
applied	002_due_dates_and_unowned_tasks
migration apply completed: 1 applied
generated internal/store
```

Review the generated diff. `rasql.sum` gained a `migration` line for the new
directory, and its `output` lines for `members_gen.go` and `tasks_gen.go`
changed to match the regenerated Go; every other line is untouched.

The application code changes only after the generated package has the needed
typed symbols. A nullable database column is represented in rows by
`rasql.Nullable` and in the graph by `LoadedOne`; the repository preserves
both distinctions.

## Follow the compiler

Run the tests against the regenerated store before touching any application
code:

```sh
go vet ./...
```

```text
# example.com/taskboard/internal/store [example.com/taskboard/internal/store.test]
internal/store/repository_graph_test.go:264:34: invalid operation: task.Assignee.Value.ID != task.Row.AssigneeID (mismatched types int64 and rasql.Nullable[int64])
```

That is the only place anything fails to build. `TasksRow.AssigneeID` was an
`int64`; it is now `rasql.Nullable[int64]`, and the one line in the graph
fixture that compared it directly to a plain `int64` no longer type-checks.
Everywhere else in the repository reads `assignee_id` through the
`TasksAssigneeEdge` relationship or writes it through the generated setter,
and neither of those signatures changed, so `go build ./...` never noticed
the column move. Fix the comparison to read the nullable value's `.Value`
field instead of comparing the field directly; `go vet ./...` and
`go test ./...` pass again once that one line does.

The compiler forced only that one fix. Letting a task go unowned is a
capability nobody has asked for yet, so `AddTask` gains it deliberately
instead of by compile error:

<!-- INCLUDE(sample/taskboard/internal/store/repository.go#addtask) -->
```go
// AddTask files one open task against projectID. A nil assigneeID files it
// with nobody on it.
func (repository Repository) AddTask(ctx context.Context, projectID int64, assigneeID *int64, title string) error {
	create := NewTasksCreate().ProjectID(projectID).Title(title).DefaultIsOpen().DefaultCreatedAt()
	if assigneeID == nil {
		create = create.ClearAssigneeID()
	} else {
		create = create.AssigneeID(*assigneeID)
	}
	plan, err := create.Plan()
	if err != nil {
		return fmt.Errorf("plan insert task %q: %w", title, err)
	}
	if _, err := rasql.ExecMutation(ctx, repository.executor, plan); err != nil {
		return fmt.Errorf("insert task %q: %w", title, err)
	}
	return nil
}
```
source: [sample/taskboard/internal/store/repository.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/repository.go)
<!-- END INCLUDE -->

`assigneeID` changes from a required `int64` to `*int64`, and a nil pointer
now means `ClearAssigneeID` instead of a missing argument. The web layer
picks up the same idea: an empty `assignee_id` in the form is no longer a
bad request.

<!-- INCLUDE(sample/taskboard/internal/web/taskboard.go#empty_assignee) -->
```go
// An empty assignee_id is the form's way of saying nobody owns this yet.
var assigneeID *int64
if raw := r.FormValue("assignee_id"); raw != "" {
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		http.Error(w, "assignee_id must be a number", http.StatusBadRequest)
		return
	}
	assigneeID = &parsed
}
```
source: [sample/taskboard/internal/web/taskboard.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/web/taskboard.go)
<!-- END INCLUDE -->

[Chapter 8](08-extend.md) is where the due date gets the same treatment, as
a typed report rather than a compile fix.

## Try reverting

`./scripts/generate.sh` already applied `002_due_dates_and_unowned_tasks` to
the running application's own database, the one `TASKBOARD_DSN` names, since
that is the only database this project has:

```sh
./scripts/migrate.sh status
```

```text
applied	001_initial
  irreversible
applied	002_due_dates_and_unowned_tasks
```

`002_due_dates_and_unowned_tasks` carries no `irreversible` line: it has
`.down.sql` sources, so `status` reports it as a migration that can be
undone. Revert it back to `001_initial`:

```sh
./scripts/migrate.sh revert -to 001_initial
```

```text
reverted	002_due_dates_and_unowned_tasks
migration revert completed: 1 reverted
```

```sh
./scripts/migrate.sh status
```

```text
applied	001_initial
  irreversible
pending	002_due_dates_and_unowned_tasks
```

The table is back to the shape chapter 3 built: no `due_on`, `assignee_id`
required again. The generated store and the repository were built for the
schema with the due date and the nullable assignee, and neither one changed
when the migration reverted, so they now describe columns and constraints
the database no longer has. Start the server against this database, with a
project and a task already in it, and ask it for the page:

```sh
go run ./cmd/taskboard
```

```text
level=ERROR msg="taskboard request failed" operation="read open projects" error="read open projects: rasql: execute query: ERROR: column task.due_on does not exist (SQLSTATE 42703)"
```

The browser gets a 500 and the body `taskboard is unavailable`; the log line
above is what a reader watching the server's own output sees. `OpenProjects`
is the handler's first database call, and its task query is a whole-row
projection that names `due_on` along with every other column, so it is the
first thing to fail once that column is gone; the page never reaches the
overdue count or the task list. Reapply to leave the database, and the
running application, working again:

```sh
./scripts/migrate.sh apply
```

```text
applied	002_due_dates_and_unowned_tasks
migration apply completed: 1 applied
```

```sh
./scripts/migrate.sh status
```

```text
applied	001_initial
  irreversible
applied	002_due_dates_and_unowned_tasks
```

The page loads again. A revert is a real operation against the database, not
a preview of one, and the code and the schema have to move together; nothing
here keeps the generated store version-aware of a database that has moved
out from under it.

## Keep reads stable

The root page remains keyed by project ID. Child tasks retain their ID order
and per-project limit. A schema change can add a field to generated whole-row
projections without changing the page's root query or cursor contract.
