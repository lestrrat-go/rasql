package store

import (
	"context"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/sample/taskboard/internal/taskboard"
)

// Repository stores Taskboard data through rasql.
type Repository struct {
	client rasql.Client
}

// New creates a Taskboard repository for client.
func New(client rasql.Client) Repository {
	return Repository{client: client}
}

// SeedDemo writes the example's initial Taskboard data.
func (repository Repository) SeedDemo(ctx context.Context) error {
	existing, err := rasql.DecodeFrom[member](repository.client, members).
		Project(
			query.Project(members.ID),
			query.Project(members.Name),
			query.Project(members.Email),
		).
		Limit(1).
		All(ctx)
	if err != nil {
		return fmt.Errorf("read existing demo members: %w", err)
	}
	if len(existing) > 0 {
		return nil
	}
	for _, member := range []member{
		{ID: 1, Name: "Ada Lovelace", Email: "ada@example.com"},
		{ID: 2, Name: "Grace Hopper", Email: "grace@example.com"},
	} {
		if _, err := rasql.Insert(ctx, repository.client, members, member); err != nil {
			return fmt.Errorf("insert member %q: %w", member.Email, err)
		}
	}

	project := project{ID: 100, OwnerID: 1, Name: "Website refresh"}
	if _, err := rasql.Insert(ctx, repository.client, projects, project); err != nil {
		return fmt.Errorf("insert project: %w", err)
	}

	for _, task := range []task{
		{ID: 1, ProjectID: project.ID, AssigneeID: 1, Title: "Draft rollout plan", Status: "todo", Priority: 1},
		{ID: 2, ProjectID: project.ID, AssigneeID: 2, Title: "Review onboarding emails", Status: "todo", Priority: 2},
		{ID: 3, ProjectID: project.ID, AssigneeID: 1, Title: "Archive old mockups", Status: "done", Priority: 3},
	} {
		if _, err := rasql.Insert(ctx, repository.client, tasks, task); err != nil {
			return fmt.Errorf("insert task %q: %w", task.Title, err)
		}
	}

	started := task{
		ID:         1,
		ProjectID:  project.ID,
		AssigneeID: 1,
		Title:      "Draft rollout plan",
		Status:     "in_progress",
		Priority:   1,
	}
	if _, err := rasql.Update(ctx, repository.client, tasks, started); err != nil {
		return fmt.Errorf("start task: %w", err)
	}
	return nil
}

// OpenTasks returns the unfinished tasks for projectID in display order.
func (repository Repository) OpenTasks(ctx context.Context, projectID int64) ([]taskboard.Summary, error) {
	return rasql.DecodeFrom[taskboard.Summary](repository.client, tasks).
		Join(rasql.InnerJoin(projects, query.Equal(tasks.ProjectID, projects.ID))).
		Project(
			query.Project(tasks.Title),
			query.Project(tasks.Priority),
		).
		Where(query.And(
			query.Equal(projects.ID, query.Bind(projectID)),
			query.NotEqual(tasks.Status, query.Bind("done")),
		)).
		Order(query.Asc(tasks.Priority), query.Asc(tasks.ID)).
		All(ctx)
}
