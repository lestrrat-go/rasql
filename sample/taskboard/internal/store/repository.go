package store

// The generated files beside this one come from the checked-in migrations,
// through scripts/generate.sh. The directive lives here because every other
// file in this package is generated, and a regenerating run would overwrite
// it there.
//
//go:generate ../../scripts/generate.sh

import (
	"context"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/query"
	"example.com/taskboard/internal/taskboard"
)

// Repository stores Taskboard data through rasql.
type Repository struct {
	db rasql.DB
}

// New creates a Taskboard repository for db.
func New(db rasql.DB) Repository {
	return Repository{db: db}
}

// SeedDemo writes the example's initial Taskboard data.
func (repository Repository) SeedDemo(ctx context.Context) error {
	members := Members()
	projects := Projects()
	tasks := Tasks()
	existing, err := rasql.DecodeFrom[MembersRow](members).
		Project(
			query.Project(members.ID()),
			query.Project(members.Name()),
			query.Project(members.Email()),
		).
		Limit(1).
		All(ctx, repository.db)
	if err != nil {
		return fmt.Errorf("read existing demo members: %w", err)
	}
	if len(existing) > 0 {
		return nil
	}
	for _, member := range []MembersRow{
		{ID: 1, Name: "Ada Lovelace", Email: "ada@example.com"},
		{ID: 2, Name: "Grace Hopper", Email: "grace@example.com"},
	} {
		if _, err := rasql.Insert(ctx, repository.db, members, member); err != nil {
			return fmt.Errorf("insert member %q: %w", member.Email, err)
		}
	}

	project := ProjectsRow{ID: 100, OwnerID: 1, Name: "Website refresh"}
	if _, err := rasql.Insert(ctx, repository.db, projects, project); err != nil {
		return fmt.Errorf("insert project: %w", err)
	}

	for _, task := range []TasksRow{
		{ID: 1, ProjectID: project.ID, AssigneeID: 1, Title: "Draft rollout plan", Status: "todo", Priority: 1},
		{ID: 2, ProjectID: project.ID, AssigneeID: 2, Title: "Review onboarding emails", Status: "todo", Priority: 2},
		{ID: 3, ProjectID: project.ID, AssigneeID: 1, Title: "Archive old mockups", Status: "done", Priority: 3},
	} {
		if _, err := rasql.Insert(ctx, repository.db, tasks, task); err != nil {
			return fmt.Errorf("insert task %q: %w", task.Title, err)
		}
	}

	started := TasksRow{
		ID:         1,
		ProjectID:  project.ID,
		AssigneeID: 1,
		Title:      "Draft rollout plan",
		Status:     "in_progress",
		Priority:   1,
	}
	if _, err := rasql.Update(ctx, repository.db, tasks, started); err != nil {
		return fmt.Errorf("start task: %w", err)
	}
	return nil
}

// OpenTasks returns up to limit unfinished tasks for projectID in display order.
func (repository Repository) OpenTasks(ctx context.Context, projectID int64, limit int, offset int) ([]taskboard.Summary, error) {
	tasks := Tasks()
	projects := Projects()
	return rasql.DecodeFrom[taskboard.Summary](tasks).
		Join(rasql.InnerJoin(projects, query.Equal(tasks.ProjectID(), projects.ID()))).
		Project(
			query.Project(tasks.Title()),
			query.Project(tasks.Priority()),
		).
		Where(query.And(
			query.Equal(projects.ID(), query.Bind(projectID)),
			query.NotEqual(tasks.Status(), query.Bind("done")),
		)).
		Order(query.Asc(tasks.Priority()), query.Asc(tasks.ID())).
		Limit(limit).
		Offset(offset).
		All(ctx, repository.db)
}
