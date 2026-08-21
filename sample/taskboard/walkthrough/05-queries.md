# 5. Build the repository

[Chapter 4](04-generate.md) generated the types. This chapter writes the operations the page needs on top of them: reading the open tasks, adding a task, closing one, and reading the two lists the add form offers.

## Where the repository lives

The hand-written code goes in `internal/store/repository.go`, beside the generated files and in the same `store` package. It needs `TasksTable`, `TasksRow`, and the relationship accessors, and nothing outside this package does.

That leaves one file in the package the generator does not write, which is where the `go:generate` directive belongs:

<!-- INCLUDE(sample/taskboard/internal/store/repository.go#generate) -->
```go
// The generated files beside this one are rebuilt from the checked-in
// migrations by scripts/generate.sh. The directive lives here because every
// other file in this package is generated, and a regenerating run would
// overwrite it there.
//
//go:generate ../../scripts/generate.sh
```
source: [sample/taskboard/internal/store/repository.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/repository.go)
<!-- END INCLUDE -->

The repository itself is a value over a `rasql.DB`:

<!-- INCLUDE(sample/taskboard/internal/store/repository.go#repository) -->
```go
// Repository reads and writes Taskboard's tables through rasql.
type Repository struct {
	db rasql.DB
}

// New creates a repository over db.
func New(db rasql.DB) Repository {
	return Repository{db: db}
}
```
source: [sample/taskboard/internal/store/repository.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/repository.go)
<!-- END INCLUDE -->

A `rasql.DB` pairs a database handle with the dialect used to render SQL, and it is a plain value, so there is nothing here to close. [Chapter 6](06-web.md) is where one gets built.

## Read the open tasks

The page shows a task's title, the project it sits under, and the owner's name. Those come from three tables, so the result has a shape no single table's row type describes. Declare that shape:

```text
// OpenTask is one line of the page's list: an open task, the project it
// sits under, and the member who owns it.
type OpenTask struct {
	ProjectID    int64
	ProjectName  string
	TaskID       int64
	Title        string
	AssigneeName string
}
```

`rasql.SelectFrom` decodes into a table's own row type. `rasql.DecodeFrom` takes the type instead, which is what a join needs:

```text
// OpenTasks returns every open task, ordered by project and then by task,
// which is the order the page prints them in.
func (repository Repository) OpenTasks(ctx context.Context) ([]OpenTask, error) {
	tasks := Tasks()
	projects := Projects()
	members := Members()
	rows, err := rasql.DecodeFrom[OpenTask](tasks).
		Join(tasks.Project().Join(), tasks.Assignee().Join()).
		Project(
			query.Project(tasks.ProjectID()).As("project_id"),
			query.Project(projects.Name()).As("project_name"),
			query.Project(tasks.ID()).As("task_id"),
			query.Project(tasks.Title()).As("title"),
			query.Project(members.Name()).As("assignee_name"),
		).
		Where(query.Equal(tasks.IsOpen(), query.Bind(true))).
		Order(query.Asc(tasks.ProjectID()), query.Asc(tasks.ID())).
		All(ctx, repository.db)
	if err != nil {
		return nil, fmt.Errorf("read open tasks: %w", err)
	}
	return rows, nil
}
```

Four things in that call are worth stopping on.

`tasks.Project().Join()` and `tasks.Assignee().Join()` are the accessors chapter 4 generated from the two foreign keys. Neither the join condition nor the direction is written out here, because the foreign key already said both. Both are inner joins, which is correct only because both columns are required; [chapter 2](02-database.md#the-assignee-required-or-nullable) is where that was settled, and chapter 7 is where it stops being true.

Each projection carries `.As(...)` naming the result column, and each of those names maps onto a field of `OpenTask`. Two of the five columns are called `name` in their own table, so without the aliases the result would have two columns of the same name and no way to tell which field each belongs to.

`query.Bind(true)` keeps the value out of the SQL text. It becomes an argument.

The ordering is `project_id` then `id`, not project *name*. That is the order [chapter 2's partial index](02-database.md#the-index-over-the-open-tasks-or-over-all-of-them) is built in, and it is enough for the page, which groups consecutive rows rather than sorting them itself.

The builder can render without executing, which is how to see what it produces:

```text
SELECT "tasks"."project_id" AS "project_id", "projects"."name" AS "project_name", "tasks"."id" AS "task_id", "tasks"."title" AS "title", "members"."name" AS "assignee_name" FROM "tasks" INNER JOIN "projects" ON ("projects"."id" = "tasks"."project_id") INNER JOIN "members" ON ("members"."id" = "tasks"."assignee_id") WHERE ("tasks"."is_open" = $1) ORDER BY "tasks"."project_id", "tasks"."id"
true
```

One statement, one argument, both joins spelled out from the foreign keys.

## Add a task

A new task supplies three values. The other three columns are the database's business: `id` is an identity column, `is_open` defaults to true, and `created_at` defaults to `now()`.

```text
// AddTask files one open task against projectID, owned by assigneeID.
func (repository Repository) AddTask(ctx context.Context, projectID int64, assigneeID int64, title string) error {
	tasks := Tasks()
	row := TasksRow{ProjectID: projectID, AssigneeID: assigneeID, Title: title}
	if _, err := rasql.InsertWithOptions(ctx, repository.db, tasks, row,
		rasql.DefaultColumns("is_open", "created_at"),
	); err != nil {
		return fmt.Errorf("insert task %q: %w", title, err)
	}
	return nil
}
```

`rasql.DefaultColumns` names the two columns to leave out of the statement so the database supplies them. Without it, the zero values of `TasksRow` would be written instead, and every new task would arrive already closed with a creation time of the year 1.

`id` is not in that list, and it does not need to be. The descriptor chapter 4 generated marks it `Identity: schema.IdentityAlways`, and rasql's write path drops such a column from an insert on its own, because PostgreSQL rejects an explicit value for one. [Chapter 2](02-database.md#the-primary-key-identity-or-serial) is where that rejection was first seen, from the other side.

## Close a task

Closing a task changes one column of one row. `rasql.Update` writes a whole row addressed by its primary key, which would mean reading the task first only to write it back. `rasql.UpdateMany` states the change and the predicate instead:

<!-- INCLUDE(sample/taskboard/internal/store/repository.go#closetask) -->
```go
// CloseTask closes the task with taskID. Closing an already closed task
// changes nothing and reports no error.
func (repository Repository) CloseTask(ctx context.Context, taskID int64) error {
	tasks := Tasks()
	if _, err := rasql.UpdateMany(ctx, repository.db, tasks, TasksRow{IsOpen: false},
		rasql.UpdateColumns("is_open"),
		rasql.UpdateWhere(query.Equal(tasks.ID(), query.Bind(taskID))),
	); err != nil {
		return fmt.Errorf("close task %d: %w", taskID, err)
	}
	return nil
}
```
source: [sample/taskboard/internal/store/repository.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/repository.go)
<!-- END INCLUDE -->

`rasql.UpdateColumns` limits the statement to `is_open`, so the rest of the passed row is never read and its zero values never reach the database. `rasql.UpdateWhere` supplies the predicate. Naming a task that does not exist, or one that is closed already, updates no rows and reports no error, which is the right answer for a button somebody clicked twice.

## Read the projects and members

The add form offers a project and an owner to pick from. Both are whole-table reads of a row type that already exists, so both go through `rasql.SelectFrom`:

<!-- INCLUDE(sample/taskboard/internal/store/repository.go#allprojects) -->
```go
// AllProjects returns every project in id order, for the form's project list.
func (repository Repository) AllProjects(ctx context.Context) ([]ProjectsRow, error) {
	projects := Projects()
	rows, err := rasql.SelectFrom(projects).OrderAsc(projects.ID()).All(ctx, repository.db)
	if err != nil {
		return nil, fmt.Errorf("read projects: %w", err)
	}
	return rows, nil
}
```
source: [sample/taskboard/internal/store/repository.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/repository.go)
<!-- END INCLUDE -->

`AllMembers` is the same call against `members`.

## Run it

The page creates tasks and nothing else, so a project and a member have to exist before any of this does anything. Put a couple of each in by hand:

```sh
./scripts/psql.sh -c "
INSERT INTO members (name) VALUES ('Ada Lovelace'), ('Grace Hopper');
INSERT INTO projects (name) VALUES ('Website refresh'), ('Billing cleanup');
SELECT id, name FROM members ORDER BY id;
SELECT id, name FROM projects ORDER BY id;"
```

```text
 id |     name
----+--------------
  1 | Ada Lovelace
  2 | Grace Hopper
(2 rows)

 id |      name
----+-----------------
  1 | Website refresh
  2 | Billing cleanup
(2 rows)
```

Both identity columns handed out 1 and 2 without being asked for anything.

Driving the repository from here needs a `main`, and [chapter 6](06-web.md) writes the one this application keeps. A throwaway `main` under `cmd/probe`, deleted before the chapter's commit, is enough to see the three operations work: it opens the database with pgx's `database/sql` driver, wraps it with `rasql.New(database, dialect.PostgreSQL())`, and calls `AddTask` three times, then `OpenTasks`, then `CloseTask`, then `OpenTasks` again.

```text
-- after three AddTask calls
1 Website refresh  1 Draft the rollout plan     Ada Lovelace
1 Website refresh  2 Review onboarding emails   Grace Hopper
2 Billing cleanup  3 Reconcile March invoices   Ada Lovelace
-- after CloseTask(2)
1 Website refresh  1 Draft the rollout plan     Ada Lovelace
2 Billing cleanup  3 Reconcile March invoices   Ada Lovelace
```

Three inserts, three ids nobody supplied, both project names and both owner names joined in, the rows arriving grouped by project. Closing task 2 removed one line and left the rest alone.

```sh
git add internal/store/repository.go
git commit -m 'add the taskboard repository'
```

## Next

[Draw the page](06-web.md).
