# 9. Operate it

Eight chapters built the application. This one is about keeping it working: the tests that catch a change before it ships, and the four `rasql migrate` subcommands that say what a database is and put it back where it should be.

## What is worth testing

`internal/store`'s generated files need no tests of their own. They are rewritten from the schema on every change, so a test over them would pin the generator rather than the application, and `schema_gen_test.go` already validates every descriptor the generator wrote.

That leaves three things. The view model turns rows into what the page prints, and it is pure. The HTTP layer turns requests into repository calls, and it needs no database once the repository is behind an interface. The repository is the one piece whose behaviour is the database's, and it needs a real one.

## The tests that need no database

`GroupByProject` decides how the page is grouped, and [chapter 6](06-web.md#the-view-model) built it on an assumption the query has to keep:

<!-- INCLUDE(sample/taskboard/internal/taskboard/taskboard_test.go#repeated_projects) -->
```go
func TestGroupByProjectSeparatesRepeatedProjects(t *testing.T) {
	// The fold trusts the query's ORDER BY. Rows that arrive out of project
	// order produce one group per run, which is what this pins: the day
	// somebody drops the ORDER BY, this test says so.
	groups := taskboard.GroupByProject([]store.OpenTask{
		{ProjectID: 1, ProjectName: "A", TaskID: 1},
		{ProjectID: 2, ProjectName: "B", TaskID: 2},
		{ProjectID: 1, ProjectName: "A", TaskID: 3},
	})
	if len(groups) != 3 {
		t.Fatalf("GroupByProject returned %d groups, want 3", len(groups))
	}
}
```
source: [sample/taskboard/internal/taskboard/taskboard_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/taskboard/taskboard_test.go)
<!-- END INCLUDE -->

The test asserts the behaviour rather than wishing it away. Out-of-order rows produce three groups, and writing that down is what turns a silent page-layout bug into a failing test if the `ORDER BY` ever goes.

Chapter 7's two new cases get the same treatment: a task with no owner prints `taskboard.Unassigned`, and a task with no due date prints an empty string.

The HTTP tests need a repository, and [chapter 6](06-web.md#the-http-layer) declared `Reader` and `Writer` for exactly this. Writing the first test found that the constructor did not:

```text
internal/web/taskboard_test.go:59:13: undefined: web.NewHandlerFrom
```

`NewHandler` took a `store.Repository` while the struct it built held two interfaces, so nothing could stand in for either half. The fix is one line and makes the declaration true:

<!-- INCLUDE(sample/taskboard/internal/web/taskboard.go#newhandler) -->
```go
// NewHandler creates a handler over a reader and a writer. store.Repository
// satisfies both, so an application passes it twice; a test passes whatever
// it needs to stand in for either half.
func NewHandler(reader Reader, writer Writer, logger *slog.Logger) Handler {
	return Handler{reader: reader, writer: writer, logger: logger}
}
```
source: [sample/taskboard/internal/web/taskboard.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/web/taskboard.go)
<!-- END INCLUDE -->

`main` now says `web.NewHandler(repository, repository, logger)`, which is repetitive and honest about what is going on. That change is the application's, not the tests', so it lands on its own:

```sh
git add internal/web/taskboard.go cmd/taskboard/main.go
git commit -m 'take the handler dependencies as interfaces'
```

The fake is a struct with the six methods and a few fields to record what it was asked:

<!-- INCLUDE(sample/taskboard/internal/web/taskboard_test.go#add_no_owner) -->
```go
func TestAddTaskWithNoOwner(t *testing.T) {
	repository := &fakeRepository{}
	form := url.Values{"project_id": {"1"}, "assignee_id": {""}, "title": {"Find an owner"}}
	request := httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	newTestHandler(repository).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST /tasks returned %d, want 303", recorder.Code)
	}
	if repository.addedTitle != "Find an owner" {
		t.Errorf("AddTask got title %q, want \"Find an owner\"", repository.addedTitle)
	}
	if repository.addedOwner != nil {
		t.Errorf("AddTask got owner %v, want nil for an empty assignee_id", *repository.addedOwner)
	}
}
```
source: [sample/taskboard/internal/web/taskboard_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/web/taskboard_test.go)
<!-- END INCLUDE -->

That is chapter 7's change stated as a test: an empty `assignee_id` has to reach the repository as a nil pointer and not as a `400`.

## The tests that need a database

The repository is where rasql meets PostgreSQL, and a fake would only prove that the fake agrees with itself. These tests take a real database or skip.

The whole of the setup is a transaction that gets rolled back:

<!-- INCLUDE(sample/taskboard/internal/store/repository_test.go#open_tx) -->
```go
// openTx returns a repository over a transaction that is rolled back when
// the test ends, and the rasql.DB it was built on for the tests that write a
// row the repository has no method for. rasql.Handle is satisfied by *sql.Tx
// as well as *sql.DB, so every write below reaches a real PostgreSQL server
// and none of them survives the test.
func openTx(t *testing.T) (store.Repository, rasql.DB) {
	t.Helper()
	dsn := os.Getenv("TASKBOARD_TEST_DSN")
	if dsn == "" {
		t.Skip("set TASKBOARD_TEST_DSN to a migrated PostgreSQL database to run this test")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TASKBOARD_TEST_DSN: %s", err)
	}
	database := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = database.Close() })

	tx, err := database.BeginTx(t.Context(), &sql.TxOptions{})
	if err != nil {
		t.Fatalf("begin: %s", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })

	db, err := rasql.New(tx, dialect.PostgreSQL())
	if err != nil {
		t.Fatalf("create the rasql db: %s", err)
	}
	return store.New(db), db
}
```
source: [sample/taskboard/internal/store/repository_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/repository_test.go)
<!-- END INCLUDE -->

`rasql.New` takes anything satisfying `rasql.Handle`, and `*sql.Tx` satisfies it as well as `*sql.DB` does. The repository never learns which one it got. Every insert and update below runs against a real server and none of them is committed, so the tests can be run against a database that has data in it without changing it.

An unset `TASKBOARD_TEST_DSN` skips rather than fails, so `go test ./...` works on a laptop with no server running.

`openTx` hands back the `rasql.DB` alongside the repository because one test needs a row the repository cannot file. `AddTask` leaves `due_on` alone, and a test about due dates has to set one, so it writes through the generated table itself:

<!-- INCLUDE(sample/taskboard/internal/store/repository_test.go#add_task_due_on) -->
```go
// addTaskDueOn files one open task with a due date. AddTask leaves due_on
// alone, so a test that needs one writes the row itself, through the same
// generated table the repository uses and over the same rolled-back
// transaction.
func addTaskDueOn(ctx context.Context, t *testing.T, db rasql.DB, projectID int64, assigneeID int64, title string, dueOn time.Time) {
	t.Helper()
	row := store.TasksRow{ProjectID: projectID, AssigneeID: &assigneeID, Title: title, DueOn: &dueOn}
	if _, err := rasql.InsertWithOptions(ctx, db, store.Tasks(), row,
		rasql.DefaultColumns("is_open", "created_at"),
	); err != nil {
		t.Fatalf("insert task %q: %s", title, err)
	}
}
```
source: [sample/taskboard/internal/store/repository_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/repository_test.go)
<!-- END INCLUDE -->

That is the same table `OpenTasks` reads and the same transaction everything else in the test runs in, so the row is rolled back with the rest.

The tests are the three operations and the one thing a fake cannot check, which is what the database does with a `NULL`:

<!-- INCLUDE(sample/taskboard/internal/store/repository_test.go#null_assignee) -->
```go
if owned.AssigneeName == nil {
	t.Error("the owned task came back with no assignee name")
}
if unowned.AssigneeName != nil {
	t.Errorf("the unowned task came back with assignee %q, want none", *unowned.AssigneeName)
}
```
source: [sample/taskboard/internal/store/repository_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/repository_test.go)
<!-- END INCLUDE -->

`CountOverdue` gets two tests of its own, because it is the one query in the application the compiler does not check against the schema. Chapter 8 named that as the price of declaring SQL; these are what pay it. The first asserts a relationship rather than a number, so it holds whatever the database already contains:

<!-- INCLUDE(sample/taskboard/internal/store/repository_test.go#overdue_unmoved) -->
```go
// AddTask files a task with no due date, so the count must not move.
if err := repository.AddTask(ctx, projectID, &memberID, "No due date"); err != nil {
	t.Fatalf("add a task: %s", err)
}
after, err := repository.CountOverdue(ctx, time.Now())
```
source: [sample/taskboard/internal/store/repository_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/repository_test.go)
<!-- END INCLUDE -->

The second pins the boundary that [chapter 8's cast](08-extend.md#2-a-declared-query-in-rasqljson) draws, which is the part of the count a reader is most likely to get wrong and the database is least likely to complain about. It hands `CountOverdue` an instant of its own choosing rather than the clock, which is what turns "a task due today is not late yet" into something a test can state:

<!-- INCLUDE(sample/taskboard/internal/store/repository_test.go#overdue_boundary) -->
```go
// TestCountOverdueCountsATaskOnlyAfterItsDueDate pins the boundary that
// CountOverdue's cast draws. due_on is a date and on is an instant, so the two
// meet only once one of them is narrowed, and narrowing on is what keeps a
// task due today out of the count until the day is over. Naming the instant
// rather than reading the clock is what the bound parameter is for.
//
// The instant carries a location as well as a day, and the count follows the
// location. Eight in the evening on 2026-03-16 in a zone nine hours behind UTC
// is already 2026-03-17 in UTC, so the day the caller is having is the one the
// count has to use, and an instant that late in its own day is also the case a
// comparison against the raw timestamp gets wrong.
func TestCountOverdueCountsATaskOnlyAfterItsDueDate(t *testing.T) {
	ctx := t.Context()
	repository, db := openTx(t)
	projectID, memberID := seed(ctx, t, repository)

	zone := time.FixedZone("UTC-9", -9*60*60)
	on := time.Date(2026, 3, 16, 20, 0, 0, 0, zone)
	today := time.Date(2026, 3, 16, 0, 0, 0, 0, zone)
	yesterday := today.AddDate(0, 0, -1)

	before, err := repository.CountOverdue(ctx, on)
	if err != nil {
		t.Fatalf("count overdue tasks: %s", err)
	}

	addTaskDueOn(ctx, t, db, projectID, memberID, "Due today", today)
	afterToday, err := repository.CountOverdue(ctx, on)
	if err != nil {
		t.Fatalf("count overdue tasks: %s", err)
	}
	if afterToday != before {
		t.Errorf("the overdue count moved from %d to %d after adding a task due today; a task is past its due date only once the day is over", before, afterToday)
	}

	addTaskDueOn(ctx, t, db, projectID, memberID, "Due yesterday", yesterday)
	afterYesterday, err := repository.CountOverdue(ctx, on)
	if err != nil {
		t.Fatalf("count overdue tasks: %s", err)
	}
	if afterYesterday != before+1 {
		t.Errorf("the overdue count went from %d to %d after adding a task due yesterday, want %d", before, afterYesterday, before+1)
	}

	addTaskDueOn(ctx, t, db, projectID, memberID, "Closed and late", yesterday)
	if err := repository.CloseTask(ctx, openTaskID(ctx, t, repository, "Closed and late")); err != nil {
		t.Fatalf("close the late task: %s", err)
	}
	afterClosed, err := repository.CountOverdue(ctx, on)
	if err != nil {
		t.Fatalf("count overdue tasks: %s", err)
	}
	if afterClosed != afterYesterday {
		t.Errorf("the overdue count moved from %d to %d after a late task was closed; a closed task is nobody's problem", afterYesterday, afterClosed)
	}
}
```
source: [sample/taskboard/internal/store/repository_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/repository_test.go)
<!-- END INCLUDE -->

Three assertions, and each one fails on a different mistake. Comparing the raw instant against the `date` column promotes the date to midnight and makes today's task late; reading the day in UTC rather than in the caller's location moves the whole boundary by a day; dropping `is_open` from the query counts work somebody already finished.

## Run them

Without a database, the live tests skip and everything else runs:

```sh
go test ./...
```

```text
?   	example.com/taskboard/cmd/taskboard	[no test files]
ok  	example.com/taskboard/internal/store	0.004s
ok  	example.com/taskboard/internal/taskboard	0.002s
ok  	example.com/taskboard/internal/web	0.004s
```

With one, the repository tests run for real:

```sh
TASKBOARD_TEST_DSN="$TASKBOARD_DSN" go test ./internal/store/ -v -count=1
```

```text
=== RUN   TestRasqlgenGeneratedDefinitionsAreValid
--- PASS: TestRasqlgenGeneratedDefinitionsAreValid (0.00s)
=== RUN   TestAddTaskAndCloseTask
--- PASS: TestAddTaskAndCloseTask (0.02s)
=== RUN   TestCloseTaskOnAMissingTaskIsNotAnError
--- PASS: TestCloseTaskOnAMissingTaskIsNotAnError (0.01s)
=== RUN   TestCountOverdue
--- PASS: TestCountOverdue (0.01s)
=== RUN   TestCountOverdueCountsATaskOnlyAfterItsDueDate
--- PASS: TestCountOverdueCountsATaskOnlyAfterItsDueDate (0.01s)
PASS
ok  	example.com/taskboard/internal/store	0.051s
```

`TestRasqlgenGeneratedDefinitionsAreValid` is the generator's own test from `schema_gen_test.go`, and it needs no server, so it is the one test in that package that ran in the block above as well.

The task count is the same before and after that run, which is the rollback doing its job:

```sh
./scripts/psql.sh -tAc "SELECT count(*) FROM tasks;"
```

```text
5
```

```sh
git add -A
git commit -m 'add the taskboard tests'
```

## The two gates

A test suite is one of two things worth running before a change ships. The other is `./scripts/generate.sh -check`, which reports whether the checked-in store still matches what the migrations produce:

```text
migration apply completed: 0 applied
internal/store is up to date
```

Those two catch different failures. The tests catch code that stopped working. The check catches a migration somebody added without regenerating, which compiles perfectly and describes the wrong database.

In this repository, `go build ./...` for the sample runs in the `check` job, and the sample's `go test ./...` and `./scripts/generate.sh -check` run in the `integration` job, where a live PostgreSQL is already up.

## Say what a database is

`rasql migrate status` compares the migrations on disk with the history table:

```sh
./scripts/migrate.sh status
```

```text
applied	001_initial
applied	002_due_dates_and_unowned_tasks
```

It reports five states. `applied` and `pending` are the ordinary two. `changed` means a migration's forward sources no longer match the checksum recorded when it was applied, which is somebody editing history. `out_of_order` means a migration sorts before one already applied, which is two branches merged carelessly. `unknown` means the history remembers a migration the directory no longer has.

`verify` is the same comparison as an exit code, which is what a deployment script wants:

```sh
./scripts/migrate.sh verify
```

```text
migration verification passed
```

It succeeds only when every supplied migration is `applied`. Anything else, and it fails.

## Undo one

`revert` runs a migration's `.down.sql` sources and deletes its history record. It requires exactly one of `-to` and `-steps`, so a run that names no target reverts nothing rather than picking a depth on its own.

Look before leaping. `-dry-run` prints the reverse SQL without opening a transaction:

```sh
./scripts/migrate.sh revert -steps 1 -dry-run
```

```text
-- 002_due_dates_and_unowned_tasks/003_assignee_on_delete_set_null.down.sql
ALTER TABLE "tasks"
  DROP CONSTRAINT "tasks_assignee_id_fkey",
  ADD CONSTRAINT "tasks_assignee_id_fkey"
    FOREIGN KEY ("assignee_id") REFERENCES "members" ("id") ON DELETE NO ACTION;

-- 002_due_dates_and_unowned_tasks/002_relax_assignee.down.sql
ALTER TABLE "tasks" ALTER COLUMN "assignee_id" SET NOT NULL;

-- 002_due_dates_and_unowned_tasks/001_add_due_on.down.sql
ALTER TABLE "tasks" DROP COLUMN "due_on";
```

That is [chapter 7's](07-change.md#write-this-migration-by-hand) three steps in descending filename order, which is the order that works: the foreign key goes back to `NO ACTION` before the column goes back to `NOT NULL`.

Read the middle statement again before running any of it. `TASKBOARD_DSN` still names `taskboard_walkthrough`, the working database, and chapter 7 filed a task there that nobody owns. Run the revert against it and `SET NOT NULL` finds that row:

```sh
./scripts/migrate.sh revert -steps 1
```

```text
migrate: execute migration "002_due_dates_and_unowned_tasks" reverse SQL source "002_relax_assignee.down.sql": ERROR: column "assignee_id" of relation "tasks" contains null values (SQLSTATE 23502)
```

That is what relaxing a column costs. The migration is reversible only while nothing has taken up the freedom it granted, and chapter 7 took it up on the same page that granted it. `revert` runs the whole migration in one transaction, so the database is where it was before the command:

```sh
./scripts/migrate.sh status
```

```text
applied	001_initial
applied	002_due_dates_and_unowned_tasks
```

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

`due_on` is still there, `assignee_id` is still nullable, and the foreign key still reads `ON DELETE SET NULL`, even though the statement that put it back to `NO ACTION` had already succeeded when the next one failed. A half-reverted schema is the thing that would have been hard to clean up, and there isn't one.

So point the migrate calls at the schema database, which was built by applying these same migrations and holds no rows for `SET NOT NULL` to find:

```sh
export TASKBOARD_DSN='postgres://rasql:rasql@127.0.0.1:5432/taskboard_verify?sslmode=disable'
```

Now do it:

```sh
./scripts/migrate.sh revert -steps 1
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
pending	002_due_dates_and_unowned_tasks
```

```sh
./scripts/migrate.sh verify
```

```text
verify migrations: migration "002_due_dates_and_unowned_tasks" is pending
```

`verify` exits non-zero, which is a deployment refusing to start against a database one migration behind the code.

The other gate notices too. Ask the generator whether the checked-in store matches this database now:

```sh
rasql codegen generate -dsn "$TASKBOARD_DSN" -check
```

```text
generate: generated package is stale: internal/store/members_gen.go: differs; internal/store/schema_gen.go: differs; internal/store/tasks_gen.go: differs
```

Three files, not one. `tasks_gen.go` lost `due_on` and got its non-nullable `assignee_id` back, `schema_gen.go` holds the descriptor that says so, and `members_gen.go` changed because `MembersTable.Tasks()` comes back the moment `assignee_id` is required again. Chapter 7 watched those three files change in the other direction.

Reverting was the demonstration, so put it back:

```sh
./scripts/migrate.sh apply
./scripts/migrate.sh verify
./scripts/generate.sh -check
```

```text
applied	002_due_dates_and_unowned_tasks
migration apply completed: 1 applied
migration verification passed
migration apply completed: 0 applied
internal/store is up to date
```

Both gates are green again. Put the working database back in front of the shell, so the next `./scripts/migrate.sh` goes where the rest of the walkthrough sent it:

```sh
export TASKBOARD_DSN='postgres://rasql:rasql@127.0.0.1:5432/taskboard_walkthrough?sslmode=disable'
```

A reverted migration becomes `pending` again, so `apply` runs it once more. That is the fix-and-reapply loop: revert it, correct its sources, apply it again. It is available only while the migration is the newest applied one and nothing depends on it, which is why the review before the first `apply` is the one that matters.

## What the eighteen commits add up to

The schema was argued for against a running server, captured into a migration, and proved to rebuild what it came from. The Go that reads it was generated from that migration, and a script says so on demand. The application was built in three layers that each know one thing. When the requirements moved, the compiler walked through the consequences and the one it could not catch failed loudly with the column named.

None of that came from a framework deciding how the application should be laid out. It came from putting the schema first and generating the parts that follow from it.

## Where to go next

[Migrations](../../../docs/core/07-migrations.md) is the reference for every `rasql migrate` subcommand, including `diff` and `diff-live`, which this walkthrough did not need.

[`rasql codegen`](../../../docs/orm/01-codegen.md) is the reference for the generator and every setting `rasql.json` takes.

[Typed queries](../../../docs/orm/03-typed-queries.md) enumerates the builder methods chapter 5 used a handful of, and [The SQL builder](../../../docs/core/02-sql-builder.md) covers the layer underneath them.
