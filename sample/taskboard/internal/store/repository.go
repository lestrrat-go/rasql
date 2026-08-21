package store

// BEGIN(generate)
// The generated files beside this one are rebuilt from the checked-in
// migrations by scripts/generate.sh. The directive lives here because every
// other file in this package is generated, and a regenerating run would
// overwrite it there.
//
//go:generate ../../scripts/generate.sh
// END(generate)

import (
	"context"
	"fmt"
	"time"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/query"
)

// BEGIN(repository)
// Repository reads and writes Taskboard's tables through rasql.
type Repository struct {
	db rasql.DB
}

// New creates a repository over db.
func New(db rasql.DB) Repository {
	return Repository{db: db}
}

// END(repository)

// BEGIN(opentask)
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

// END(opentask)

// OpenTasks returns every open task, ordered by project and then by task,
// which is the order the page prints them in.
func (repository Repository) OpenTasks(ctx context.Context) ([]OpenTask, error) {
	tasks := Tasks()
	projects := Projects()
	members := Members()
	rows, err := rasql.DecodeFrom[OpenTask](tasks).
		// BEGIN(leftjoin)
		Join(
			tasks.Project().Join(),
			rasql.LeftJoin(members, query.Equal(members.ID(), tasks.AssigneeID())),
		).
		// END(leftjoin)
		Project(
			query.Project(tasks.ProjectID()).As("project_id"),
			query.Project(projects.Name()).As("project_name"),
			query.Project(tasks.ID()).As("task_id"),
			query.Project(tasks.Title()).As("title"),
			query.Project(members.Name()).As("assignee_name"),
			query.Project(tasks.DueOn()).As("due_on"),
		).
		Where(tasks.Open()).
		Order(query.Asc(tasks.ProjectID()), query.Asc(tasks.ID())).
		All(ctx, repository.db)
	if err != nil {
		return nil, fmt.Errorf("read open tasks: %w", err)
	}
	return rows, nil
}

// AddTask files one open task against projectID. A nil assigneeID files it
// with nobody on it.
func (repository Repository) AddTask(ctx context.Context, projectID int64, assigneeID *int64, title string) error {
	tasks := Tasks()
	row := TasksRow{ProjectID: projectID, AssigneeID: assigneeID, Title: title}
	if _, err := rasql.InsertWithOptions(ctx, repository.db, tasks, row,
		rasql.DefaultColumns("is_open", "created_at"),
	); err != nil {
		return fmt.Errorf("insert task %q: %w", title, err)
	}
	return nil
}

// BEGIN(closetask)
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

// END(closetask)

// BEGIN(allprojects)
// AllProjects returns every project in id order, for the form's project list.
func (repository Repository) AllProjects(ctx context.Context) ([]ProjectsRow, error) {
	projects := Projects()
	rows, err := rasql.SelectFrom(projects).OrderAsc(projects.ID()).All(ctx, repository.db)
	if err != nil {
		return nil, fmt.Errorf("read projects: %w", err)
	}
	return rows, nil
}

// END(allprojects)

// AllMembers returns every member in id order, for the form's member list.
func (repository Repository) AllMembers(ctx context.Context) ([]MembersRow, error) {
	members := Members()
	rows, err := rasql.SelectFrom(members).OrderAsc(members.ID()).All(ctx, repository.db)
	if err != nil {
		return nil, fmt.Errorf("read members: %w", err)
	}
	return rows, nil
}

// BEGIN(countoverdue)
// overdueRow decodes the single column OverdueCount selects.
type overdueRow struct {
	Overdue int64
}

// CountOverdue returns how many open tasks fell due before the calendar day
// on names. A task due on that day is not counted, because a task is past its
// due date only once the day is over. The query casts the bound value to a
// date, and the driver reads that date off on in on's own location, so the
// caller decides which day it is and the database session's time zone does
// not.
func (repository Repository) CountOverdue(ctx context.Context, on time.Time) (int64, error) {
	statement, err := OverdueCount(on)
	if err != nil {
		return 0, fmt.Errorf("build the overdue count: %w", err)
	}
	row, err := rasql.QueryRenderedOne[overdueRow](ctx, repository.db, statement)
	if err != nil {
		return 0, fmt.Errorf("count overdue tasks: %w", err)
	}
	return row.Overdue, nil
}

// END(countoverdue)
