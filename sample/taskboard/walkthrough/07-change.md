# 7. Change the schema

The application works. Now the requirements move.

Two changes arrive together. A task should carry a due date, because a board of undated work tells nobody what to do next. And a task should be allowed to exist with nobody on it, because work gets filed before it gets assigned, and [chapter 1](01-design.md#what-the-application-does) decided that version one would not allow that.

Both changes land in the database first. What makes this chapter worth reading is what happens afterwards: the generated code changes shape, and the compiler walks through every place the old shape was assumed.

## Write this migration by hand

[Chapter 3](03-capture.md) captured the first migration out of a live database, because the schema had been shaped by hand in `psql` and the database was the only place it existed. That is a one-time situation and it is over.

The schema now lives in `db/migrations`. Shaping the live database again and capturing the difference would mean the change existed somewhere untracked first, and it would leave the two databases this walkthrough uses disagreeing until somebody remembered to fix the other. Writing the migration first inverts that: the change exists in the repository before it exists anywhere else, and every database gets it the same way.

`rasql migrate diff` is the middle road, comparing two checked-in desired-schema directories and proposing a migration between them. It is worth reaching for on a large schema. Three `ALTER TABLE` statements do not need it.

Create the directory and write the three steps:

```sh
mkdir -p db/migrations/002_due_dates_and_unowned_tasks
```

`001_add_due_on.up.sql` adds the column:

```sql
ALTER TABLE "tasks" ADD COLUMN "due_on" date;
```

```sql
ALTER TABLE "tasks" DROP COLUMN "due_on";
```

The column is nullable, and it has to be. A required column added to a table that already holds rows needs a default to fill them with, and there is no date that would be right for work already filed. A task with no due date is a real state, not a gap.

`002_relax_assignee.up.sql` drops the constraint that made an owner mandatory:

```sql
ALTER TABLE "tasks" ALTER COLUMN "assignee_id" DROP NOT NULL;
```

```sql
ALTER TABLE "tasks" ALTER COLUMN "assignee_id" SET NOT NULL;
```

That reverse statement fails against a table that has since acquired an unowned task, which is correct: undoing this migration means going back to a world where every task has an owner, and the database refuses rather than inventing one.

`003_assignee_on_delete_set_null.up.sql` is the one [chapter 2](02-database.md#the-foreign-keys-what-a-delete-does) could not write:

```sql
ALTER TABLE "tasks"
  DROP CONSTRAINT "tasks_assignee_id_fkey",
  ADD CONSTRAINT "tasks_assignee_id_fkey"
    FOREIGN KEY ("assignee_id") REFERENCES "members" ("id") ON DELETE SET NULL;
```

```sql
ALTER TABLE "tasks"
  DROP CONSTRAINT "tasks_assignee_id_fkey",
  ADD CONSTRAINT "tasks_assignee_id_fkey"
    FOREIGN KEY ("assignee_id") REFERENCES "members" ("id") ON DELETE NO ACTION;
```

Chapter 2 established that `ON DELETE SET NULL` on a `NOT NULL` column is accepted by `CREATE TABLE` and then fails on the first delete. Version one therefore refused to delete a member who still owned tasks. Now that the column can hold nothing, the clause means what it says, and a member can leave without taking their work with them or blocking their own deletion.

The order matters, and the reverse order matters more. Forward sources run in ascending filename order, so the column is relaxed before the constraint that depends on that is added. Reverse sources run in **descending** order, so the constraint goes back to `NO ACTION` before the column goes back to `NOT NULL`, which is the only order that can work.

```sh
git add db/migrations/002_due_dates_and_unowned_tasks
git commit -m 'add due dates and let a task go unowned'
```

## Retire the hand-shaped database

The working database still carries the schema chapter 2 typed into `psql`, and it has no migration history at all, because chapter 3 applied the migration to a separate database rather than to this one. `rasql migrate apply` cannot help it: the first migration's `CREATE TABLE` would fail against tables that already exist.

That is the last consequence of shaping a database by hand, and the fix is to stop having one. Rebuild it from the migrations:

```sh
podman exec rasql-postgres psql -U rasql -d postgres \
  -c 'DROP DATABASE taskboard_walkthrough;' \
  -c 'CREATE DATABASE taskboard_walkthrough;'
```

```text
DROP DATABASE
CREATE DATABASE
```

```sh
./scripts/migrate.sh apply
```

```text
applied	001_initial
applied	002_due_dates_and_unowned_tasks
migration apply completed: 2 applied
```

```sh
./scripts/migrate.sh status
```

```text
applied	001_initial
applied	002_due_dates_and_unowned_tasks
```

Ask the server what it built:

```sh
./scripts/psql.sh -c '\d tasks'
```

```text
                                     Table "public.tasks"
   Column    |           Type           | Collation | Nullable |           Default
-------------+--------------------------+-----------+----------+------------------------------
 id          | bigint                   |           | not null | generated always as identity
 project_id  | bigint                   |           | not null |
 assignee_id | bigint                   |           |          |
 title       | text                     |           | not null |
 is_open     | boolean                  |           | not null | true
 created_at  | timestamp with time zone |           | not null | now()
 due_on      | date                     |           |          |
Indexes:
    "tasks_pkey" PRIMARY KEY, btree (id)
    "tasks_open_by_project" btree (project_id, id) WHERE is_open
Foreign-key constraints:
    "tasks_assignee_id_fkey" FOREIGN KEY (assignee_id) REFERENCES members(id) ON DELETE SET NULL
    "tasks_project_id_fkey" FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
```

`due_on` is there, `assignee_id` no longer says `not null`, and the assignee foreign key now reads `ON DELETE SET NULL`.

Put some data back, including one task nobody owns:

```sh
./scripts/psql.sh -c "
INSERT INTO members (name) VALUES ('Ada Lovelace'), ('Grace Hopper');
INSERT INTO projects (name) VALUES ('Website refresh'), ('Billing cleanup');
INSERT INTO tasks (project_id, assignee_id, title, due_on) VALUES
  (1, 1, 'Draft the rollout plan', DATE '2026-09-01'),
  (1, NULL, 'Pick a heading typeface', NULL),
  (2, 2, 'Reconcile March invoices', DATE '2026-08-25');
SELECT id, project_id, assignee_id, title, due_on FROM tasks ORDER BY id;"
```

```text
 id | project_id | assignee_id |          title           |   due_on
----+------------+-------------+--------------------------+------------
  1 |          1 |           1 | Draft the rollout plan   | 2026-09-01
  2 |          1 |             | Pick a heading typeface  |
  3 |          2 |           2 | Reconcile March invoices | 2026-08-25
(3 rows)
```

Task 2 has no owner. Nothing in the Go code knows that is possible yet.

## Regenerate

```sh
./scripts/generate.sh
```

```text
migration apply completed: 0 applied
wrote internal/store from 3 tables
```

Nothing was pending, because the schema database took the new migration on its own run of the same script. The store was rewritten from it.

Three things changed in `internal/store`. The row type gained a field and changed one:

```text
type TasksRow struct {
	ID         int64
	ProjectID  int64
	AssigneeID *int64
	Title      string
	IsOpen     bool
	CreatedAt  time.Time
	DueOn      *time.Time
}
```

The descriptor records the new nullability and the new referential action:

```text
		{Name: "assignee_id", Type: schema.IntegerType{}, Nullable: true},
		{Name: "due_on", Type: schema.TimeType{}, Nullable: true},
```

```text
		{Name: "tasks_assignee_id_fkey", Columns: []string{"assignee_id"}, ReferencedTable: "members", ReferencedColumns: []string{"id"}, OnDelete: schema.SetNull, OnUpdate: schema.NoAction},
```

And two methods are gone. `TasksTable.Assignee()` and `MembersTable.Tasks()` are no longer generated, because a generated relationship accessor requires a non-nullable child column and `assignee_id` is no longer one. The `Relationships` entry is still in the descriptor; what the generator will not write is the typed join, because there is no single correct join for an optional link. An inner join and a left join answer different questions, and only the application knows which one it wants.

`TasksTable.Project()` is untouched. `project_id` is still required.

## Follow the compiler

Build:

```sh
go build ./...
```

```text
# example.com/taskboard/internal/store
internal/store/repository.go:45:38: tasks.Assignee undefined (type TasksTable has no field or method Assignee)
internal/store/repository.go:65:52: cannot use assigneeID (variable of type int64) as *int64 value in struct literal
```

Two errors, and they are the two changes stated back in Go. Take them one at a time.

### The join that is no longer generated

`OpenTasks` asked for `tasks.Assignee().Join()`, which no longer exists. Chapter 5 said an inner join was correct only because the column was required, and it no longer is: an inner join would now drop every unowned task from the page, silently, which is the exact failure [chapter 2](02-database.md#the-assignee-required-or-nullable) showed two rows of against three.

Write the join out, as a left join:

```text
		Join(
			tasks.Project().Join(),
			rasql.LeftJoin(members, query.Equal(members.ID(), tasks.AssigneeID())),
		).
```

`tasks.Project().Join()` stays generated. Only the optional half is hand-written, and it is hand-written because a person had to choose.

Build again:

```text
# example.com/taskboard/internal/store
internal/store/repository.go:68:52: cannot use assigneeID (variable of type int64) as *int64 value in struct literal
```

### The insert that assumed an owner

`AddTask` took an `int64`. It has to take a pointer now, and a nil pointer has a meaning worth naming:

```text
// AddTask files one open task against projectID. A nil assigneeID files it
// with nobody on it.
func (repository Repository) AddTask(ctx context.Context, projectID int64, assigneeID *int64, title string) error {
```

Build again, and the error moves to a different package:

```text
# example.com/taskboard/internal/web
internal/web/taskboard.go:44:45: cannot use repository (variable of struct type store.Repository) as Writer value in struct literal: store.Repository does not implement Writer (wrong type for method AddTask)
		have AddTask(context.Context, int64, *int64, string) error
		want AddTask(context.Context, int64, int64, string) error
```

A schema change has reached the HTTP layer, and the compiler named the exact method and both signatures.

### The form that could not say "nobody"

Widen the interface to match, and then answer the question the change actually asks: what does the form send when no owner is picked? An empty string, which is not a number and never was:

```text
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

The old code returned `400` for an empty value, because an empty value was a mistake. Now it is a choice.

Build:

```sh
go build ./...
```

It passes.

## The error the compiler could not find

Start the server and ask for the page:

```text
HTTP/1.1 500 Internal Server Error
Content-Type: text/plain; charset=utf-8
Content-Length: 25

taskboard is unavailable
```

The log says what the browser is not told:

```text
level=ERROR msg="taskboard request failed" operation="read open tasks" error="read open tasks: rasql: decode row 1: row: decode column \"assignee_name\": expected string, got NULL"
```

This is the third change, and it arrived at run time because nothing in Go stated it. `OpenTask.AssigneeName` is a `string`, and the left join now produces a `NULL` for it. The compiler had no way to know: `OpenTask` is a type this application declared, not one the generator writes, so nothing tied it to the column's nullability.

Chapter 6's error handling is what made this readable. The handler logged the cause and told the browser one sentence, so a decode error naming a column did not become a page.

Make the type say what the query can now return, and take the new column while there:

```text
// OpenTask is one line of the page's list: an open task, the project it
// sits under, and the member who owns it. AssigneeName is nil when nobody
// owns the task, and DueOn is nil when it has no due date.
type OpenTask struct {
	ProjectID    int64
	ProjectName  string
	TaskID       int64
	Title        string
	AssigneeName *string
	DueOn        *time.Time
}
```

```text
			query.Project(tasks.DueOn()).As("due_on"),
```

Build, and the compiler finds the next place that assumed an owner:

```text
# example.com/taskboard/internal/taskboard
internal/taskboard/taskboard.go:45:86: cannot use row.AssigneeName (variable of type *string) as string value in struct literal
```

## Decide what "unowned" looks like

That error is in the view model, and the view model is the right place to answer it. What the page prints for a task with no owner is a presentation decision, not a storage one, so the nil never reaches the template:

```text
// Task is one open task as the page prints it. Both Assignee and DueOn are
// already the text the page shows, so the template never asks whether a
// task has an owner or a due date; this package answers that once.
type Task struct {
	ID       int64
	Title    string
	Assignee string
	DueOn    string
}

// Unassigned is what the page prints where an owner's name would go.
const Unassigned = "unassigned"

func assigneeText(name *string) string {
	if name == nil {
		return Unassigned
	}
	return *name
}

func dueText(due *time.Time) string {
	if due == nil {
		return ""
	}
	return due.Format(time.DateOnly)
}
```

The template shows the due date when there is one, and offers the new choice in the form:

```html
    {{.Title}} &mdash; {{.Assignee}}{{if .DueOn}} (due {{.DueOn}}){{end}}
```

```html
      <option value="">unassigned</option>
      {{range .Members}}<option value="{{.ID}}">{{.Name}}</option>{{end}}
```

`go build ./...` passes.

## Run it

```sh
curl -s http://127.0.0.1:18080/ | sed -n '/Taskboard<\/h1>/,/Add a task/p'
```

```text
<h1>Taskboard</h1>


<h2>Website refresh</h2>
<ul>

  <li>
    Draft the rollout plan &mdash; Ada Lovelace (due 2026-09-01)
    <form method="post" action="/tasks/1/close"><button type="submit">close</button></form>
  </li>

  <li>
    Pick a heading typeface &mdash; unassigned
    <form method="post" action="/tasks/2/close"><button type="submit">close</button></form>
  </li>

</ul>

<h2>Billing cleanup</h2>
<ul>

  <li>
    Reconcile March invoices &mdash; Grace Hopper (due 2026-08-25)
    <form method="post" action="/tasks/3/close"><button type="submit">close</button></form>
  </li>

</ul>


<h2>Add a task</h2>
```

The unowned task is on the page. File another one through the form, leaving the owner empty:

```sh
curl -s -i -X POST http://127.0.0.1:18080/tasks \
  -d 'project_id=2' -d 'assignee_id=' -d 'title=Find an owner for the audit'
```

```text
HTTP/1.1 303 See Other
Location: /
```

Then delete a member who still owns work, which version one refused outright:

```sh
./scripts/psql.sh -c "DELETE FROM members WHERE name = 'Ada Lovelace';" \
  -c "SELECT id, title, assignee_id FROM tasks ORDER BY id;"
```

```text
DELETE 1
 id |            title            | assignee_id
----+-----------------------------+-------------
  1 | Draft the rollout plan      |
  2 | Pick a heading typeface     |
  3 | Reconcile March invoices    |           2
  4 | Find an owner for the audit |
(4 rows)
```

Ada's task survived her, with nobody on it. `ON DELETE SET NULL` did what chapter 2 watched it do on a probe table, this time on the real one.

```sh
git add -A
git commit -m 'follow the schema change through the go code'
```

The regeneration and the fixes are one commit, because the tree does not build between them. A commit that does not compile is not a step anybody can stand on.

## The schema can no longer be captured

One thing this migration cost, and it is worth stating plainly. `due_on` is a `date`, and [chapter 3](03-capture.md#what-the-command-refuses-to-capture) showed that `date` is not on `rasql migrate dump`'s allow-list:

```sh
rasql migrate dump -dialect postgresql -dsn "$TASKBOARD_DSN"
```

```text
dump: table "tasks" column "due_on" is declared "date", which rasql renders as "TIMESTAMPTZ"; capturing it would build a different column, so this table cannot be dumped
```

The schema of this application now contains a column the command that produced its first migration could not produce today. The other two tables still dump:

```sh
rasql migrate dump -dialect postgresql -dsn "$TASKBOARD_DSN" -exclude tasks
```

```text
-- members.sql
CREATE TABLE "members" ("id" BIGINT NOT NULL GENERATED ALWAYS AS IDENTITY, "name" TEXT NOT NULL, PRIMARY KEY ("id"));

-- projects.sql
CREATE TABLE "projects" ("id" BIGINT NOT NULL GENERATED ALWAYS AS IDENTITY, "name" TEXT NOT NULL, PRIMARY KEY ("id"));
```

Nothing about the application is broken by this. `rasql migrate apply` sends the migration's bytes to PostgreSQL unchanged and never renders anything, so a `date` column applies exactly as written. `rasql codegen generate` reads the column and generates a `*time.Time` for it, which is what the page has been printing. What is unavailable is capture: `dump` cannot be used to produce a schema directory for `tasks`, so the `migrate diff` workflow that compares two such directories is closed for that table, and every future change to it is written the way this one was.

Weigh that when picking a type. `timestamptz` would have kept the table dumpable, at the cost of storing a time of day that a due date does not have. This application preferred the honest column and writes its own migrations, which after this chapter it was going to do anyway.

## Next

[Build on the generated code](08-extend.md).
