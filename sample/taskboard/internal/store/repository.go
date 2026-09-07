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
)

// BEGIN(repository)

// Repository reads and writes Taskboard's tables through rasql.
type Repository struct {
	executor rasql.Executor
}

// New creates a repository over executor.
func New(executor rasql.Executor) Repository {
	return Repository{executor: executor}
}

// END(repository)

// BEGIN(opentask)

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

// END(opentask)

func openProjectsPlan() (rasql.GraphPlan[ProjectsRow, openProjectGraph], rasql.TypedRelation[ProjectsRow], error) {
	projectsSource, err := Projects().Source("project")
	if err != nil {
		return rasql.GraphPlan[ProjectsRow, openProjectGraph]{}, rasql.TypedRelation[ProjectsRow]{}, err
	}
	projectsExpressions, err := (ProjectsColumns{}).Bind(projectsSource)
	if err != nil {
		return rasql.GraphPlan[ProjectsRow, openProjectGraph]{}, rasql.TypedRelation[ProjectsRow]{}, err
	}
	projectsProjection, err := ProjectsProjection(projectsExpressions)
	if err != nil {
		return rasql.GraphPlan[ProjectsRow, openProjectGraph]{}, rasql.TypedRelation[ProjectsRow]{}, err
	}
	projectsQuery := rasql.Select(projectsSource.Source(), projectsProjection).
		OrderBy(rasql.AscExpr(projectsExpressions.ID.Expr()))

	tasksSource, err := Tasks().Source("task")
	if err != nil {
		return rasql.GraphPlan[ProjectsRow, openProjectGraph]{}, rasql.TypedRelation[ProjectsRow]{}, err
	}
	tasksExpressions, err := (TasksColumns{}).Bind(tasksSource)
	if err != nil {
		return rasql.GraphPlan[ProjectsRow, openProjectGraph]{}, rasql.TypedRelation[ProjectsRow]{}, err
	}
	tasksProjection, err := TasksProjection(tasksExpressions)
	if err != nil {
		return rasql.GraphPlan[ProjectsRow, openProjectGraph]{}, rasql.TypedRelation[ProjectsRow]{}, err
	}
	tasksQuery := rasql.Select(tasksSource.Source(), tasksProjection)

	membersSource, err := Members().Source("assignee")
	if err != nil {
		return rasql.GraphPlan[ProjectsRow, openProjectGraph]{}, rasql.TypedRelation[ProjectsRow]{}, err
	}
	membersExpressions, err := (MembersColumns{}).Bind(membersSource)
	if err != nil {
		return rasql.GraphPlan[ProjectsRow, openProjectGraph]{}, rasql.TypedRelation[ProjectsRow]{}, err
	}
	membersProjection, err := MembersProjection(membersExpressions)
	if err != nil {
		return rasql.GraphPlan[ProjectsRow, openProjectGraph]{}, rasql.TypedRelation[ProjectsRow]{}, err
	}
	membersQuery := rasql.Select(membersSource.Source(), membersProjection)
	membersPlan, err := rasql.NewGraphPlan(membersQuery, func(row MembersRow) MembersRow { return row })
	if err != nil {
		return rasql.GraphPlan[ProjectsRow, openProjectGraph]{}, rasql.TypedRelation[ProjectsRow]{}, err
	}
	assigneeEdge, err := TasksAssigneeEdge(
		tasksSource,
		membersSource,
		membersPlan,
		rasql.EdgeOptions{},
		func(graph *openTaskGraph, loaded rasql.LoadedOne[MembersRow]) { graph.Assignee = loaded },
	)
	if err != nil {
		return rasql.GraphPlan[ProjectsRow, openProjectGraph]{}, rasql.TypedRelation[ProjectsRow]{}, err
	}
	tasksPlan, err := rasql.NewGraphPlan(
		tasksQuery,
		func(row TasksRow) openTaskGraph { return openTaskGraph{Row: row} },
		assigneeEdge,
	)
	if err != nil {
		return rasql.GraphPlan[ProjectsRow, openProjectGraph]{}, rasql.TypedRelation[ProjectsRow]{}, err
	}
	tasksEdge, err := ProjectsTasksEdge(
		projectsSource,
		tasksSource,
		tasksPlan,
		rasql.EdgeOptions{
			Where:          rasql.EqualValue(tasksExpressions.IsOpen.Expr(), true),
			Order:          []rasql.OrderTerm{rasql.AscExpr(tasksExpressions.ID.Expr())},
			PerParentLimit: 5,
		},
		func(graph *openProjectGraph, loaded rasql.LoadedMany[openTaskGraph]) { graph.Tasks = loaded },
	)
	if err != nil {
		return rasql.GraphPlan[ProjectsRow, openProjectGraph]{}, rasql.TypedRelation[ProjectsRow]{}, err
	}
	projectsPlan, err := rasql.NewGraphPlan(
		projectsQuery,
		func(row ProjectsRow) openProjectGraph { return openProjectGraph{Row: row} },
		tasksEdge,
	)
	if err != nil {
		return rasql.GraphPlan[ProjectsRow, openProjectGraph]{}, rasql.TypedRelation[ProjectsRow]{}, err
	}
	return projectsPlan, projectsSource, nil
}

type openTaskGraph struct {
	Row      TasksRow
	Assignee rasql.LoadedOne[MembersRow]
}

type openProjectGraph struct {
	Row   ProjectsRow
	Tasks rasql.LoadedMany[openTaskGraph]
}

// OpenProjects returns one page of open projects and their bounded task graph.
func (repository Repository) OpenProjects(ctx context.Context, request rasql.PageRequest) (OpenProjectsPage, error) {
	plan, projectsSource, err := openProjectsPlan()
	if err != nil {
		return OpenProjectsPage{}, fmt.Errorf("build open project graph: %w", err)
	}
	idKey, err := ProjectsIDPageKey(projectsSource, rasql.PageAscending)
	if err != nil {
		return OpenProjectsPage{}, fmt.Errorf("build project page key: %w", err)
	}
	spec, err := rasql.NewPageSpec([]rasql.PageKey[ProjectsRow]{idKey}, idKey)
	if err != nil {
		return OpenProjectsPage{}, fmt.Errorf("build project page: %w", err)
	}
	page, err := rasql.PageGraphAfter(ctx, repository.executor, plan, spec,
		rasql.PagePolicy{DefaultLimit: 10, MaxLimit: 50}, request)
	if err != nil {
		return OpenProjectsPage{}, fmt.Errorf("read open projects: %w", err)
	}
	result := OpenProjectsPage{Next: page.Next, HasMore: page.HasMore, Values: make([]OpenProject, 0, len(page.Values))}
	for _, project := range page.Values {
		value := OpenProject{Row: project.Row, Tasks: rasql.LoadedMany[OpenTask]{Loaded: project.Tasks.Loaded}}
		if project.Tasks.Loaded {
			value.Tasks.Values = make([]OpenTask, 0, len(project.Tasks.Values))
			for _, task := range project.Tasks.Values {
				value.Tasks.Values = append(value.Tasks.Values, OpenTask{Row: task.Row, Assignee: task.Assignee})
			}
		}
		result.Values = append(result.Values, value)
	}
	return result, nil
}

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

// BEGIN(closetask)

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

// END(closetask)

// BEGIN(allprojects)

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

// END(allprojects)

// AllMembers returns every member in id order, for the form's member list.
func (repository Repository) AllMembers(ctx context.Context) ([]MembersRow, error) {
	source, err := Members().Source("member")
	if err != nil {
		return nil, fmt.Errorf("bind members source: %w", err)
	}
	expressions, err := (MembersColumns{}).Bind(source)
	if err != nil {
		return nil, fmt.Errorf("bind members columns: %w", err)
	}
	projection, err := MembersProjection(expressions)
	if err != nil {
		return nil, fmt.Errorf("build members projection: %w", err)
	}
	q := rasql.Select(source.Source(), projection).OrderBy(rasql.AscExpr(expressions.ID.Expr()))
	sequence, err := rasql.Rows(ctx, repository.executor, q)
	if err != nil {
		return nil, fmt.Errorf("read members: %w", err)
	}
	rows := make([]MembersRow, 0)
	for row, rowErr := range sequence {
		if rowErr != nil {
			return nil, fmt.Errorf("read members: %w", rowErr)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// BEGIN(countoverdue)

// CountOverdue returns how many open tasks fell due before the calendar day
// on date. A task due on that day is not counted, because a task is past its
// due date only once the day is over. The query casts the bound value to a
// date, and the driver reads that date off on in on's own location, so the
// caller decides which day it is and the database session's time zone does
// not.
func (repository Repository) CountOverdue(ctx context.Context, on time.Time) (int64, error) {
	q, err := OverdueCount(on)
	if err != nil {
		return 0, fmt.Errorf("build overdue query: %w", err)
	}
	row, err := rasql.One(ctx, repository.executor, q)
	if err != nil {
		return 0, fmt.Errorf("count overdue tasks: %w", err)
	}
	return row.Overdue, nil
}

// END(countoverdue)
