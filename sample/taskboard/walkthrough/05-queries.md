# 5. Build the typed repository

The repository owns database access and returns finite graph values to the
presentation layer. It receives one `rasql.Executor`, so callers can give it a
transaction-scoped executor without another query builder.

<!-- INCLUDE(sample/taskboard/internal/store/repository.go#generate) -->
```go
// The generated files beside this one are rebuilt from the database
// TASKBOARD_DSN names, after applying db/migrations to it, by
// scripts/generate.sh. The directive lives here because every other file in
// this package is generated, and a regenerating run would overwrite it
// there.
//
//go:generate ../../scripts/generate.sh
```
source: [sample/taskboard/internal/store/repository.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/repository.go)
<!-- END INCLUDE -->

<!-- INCLUDE(sample/taskboard/internal/store/repository.go#repository) -->
```go
// Repository reads and writes Taskboard's tables through rasql.
type Repository struct {
	executor rasql.Executor
	hooks    *openProjectsHooks
}

// New creates a repository over executor.
func New(executor rasql.Executor) Repository {
	return Repository{executor: executor}
}
```
source: [sample/taskboard/internal/store/repository.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/repository.go)
<!-- END INCLUDE -->

## Build one graph plan

The open-project page creates one nonempty typed relation for each stage:
`projectsSource`, `tasksSource`, and `membersSource`. Each source is bound
once to its generated expression set. That same value supplies the projection,
predicates, ordering, edge factory, and page key.

<!-- INCLUDE(sample/taskboard/internal/store/repository.go#opentask) -->
```go
// OpenTask is one task attached to an open project.
type OpenTask struct {
	Row      TasksRow
	Assignee rasql.LoadedOne[MembersRow]
}

// OpenProject is one project and its bounded open-task graph.
type OpenProject struct {
	Row   ProjectsRow
	Tasks rasql.LoadedMany[OpenTask]
}

// OpenProjectsPage contains one keyset page of open projects.
type OpenProjectsPage struct {
	Values  []OpenProject
	Next    rasql.Cursor
	HasMore bool
}
```
source: [sample/taskboard/internal/store/repository.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/repository.go)
<!-- END INCLUDE -->

The project query is ordered by project ID. The task edge filters open rows,
orders by task ID, and limits each project to five tasks in SQL. The assignee
edge is optional, so the graph records loaded, absent, and present values
separately. `PageGraphAfter` executes one root page query and then expands
only retained projects.

## Write with generated inputs

`AddTask` uses the generated create input. It asks the database for the open
and creation-time defaults, and sets the assignee with the typed setter;
`assignee_id` is required, so every call must name one. No write column is
named as a string.

<!-- INCLUDE(sample/taskboard/internal/store/repository.go#closetask) -->
```go
// CloseTask closes the task with taskID. Closing an already closed task
// changes nothing and reports no error.
func (repository Repository) CloseTask(ctx context.Context, taskID int64) error {
	tasksSource, err := Tasks().Source("")
	if err != nil {
		return fmt.Errorf("bind tasks source for close %d: %w", taskID, err)
	}
	tasksExpressions, err := (TasksColumns{}).Bind(tasksSource)
	if err != nil {
		return fmt.Errorf("bind tasks columns for close %d: %w", taskID, err)
	}
	plan, err := NewTasksPatch().IsOpen(false).Where(rasql.EqualValue(tasksExpressions.ID.Expr(), taskID))
	if err != nil {
		return fmt.Errorf("plan close task %d: %w", taskID, err)
	}
	if _, err := rasql.ExecMutation(ctx, repository.executor, plan); err != nil {
		return fmt.Errorf("close task %d: %w", taskID, err)
	}
	return nil
}
```
source: [sample/taskboard/internal/store/repository.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/repository.go)
<!-- END INCLUDE -->

`CloseTask` uses a generated patch and an expression bound from a method-local
typed relation. It treats zero affected rows as success.

<!-- INCLUDE(sample/taskboard/internal/store/repository.go#allprojects) -->
```go
// AllProjects returns every project in id order, for the form's project list.
func (repository Repository) AllProjects(ctx context.Context) ([]ProjectsRow, error) {
	source, err := Projects().Source("project")
	if err != nil {
		return nil, fmt.Errorf("bind projects source: %w", err)
	}
	expressions, err := (ProjectsColumns{}).Bind(source)
	if err != nil {
		return nil, fmt.Errorf("bind projects columns: %w", err)
	}
	projection, err := ProjectsProjection(expressions)
	if err != nil {
		return nil, fmt.Errorf("build projects projection: %w", err)
	}
	q := rasql.Select(source.Source(), projection).OrderBy(rasql.AscExpr(expressions.ID.Expr()))
	sequence, err := rasql.Rows(ctx, repository.executor, q)
	if err != nil {
		return nil, fmt.Errorf("read projects: %w", err)
	}
	rows := make([]ProjectsRow, 0)
	for row, rowErr := range sequence {
		if rowErr != nil {
			return nil, fmt.Errorf("read projects: %w", rowErr)
		}
		rows = append(rows, row)
	}
	return rows, nil
}
```
source: [sample/taskboard/internal/store/repository.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/repository.go)
<!-- END INCLUDE -->

The form lists use generated whole-row projections and `rasql.Rows`. The
repository collects the iterator so callers receive ordinary slices.
