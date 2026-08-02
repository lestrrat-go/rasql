package main

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"io"
	"os"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaDDL string

type taskSummary struct {
	Title    string
	Priority int64
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "taskboard: %s\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, output io.Writer) error {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return fmt.Errorf("open SQLite database: %w", err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	if _, err := database.ExecContext(ctx, schemaDDL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		return fmt.Errorf("create rasql client: %w", err)
	}
	if err := seed(ctx, client); err != nil {
		return fmt.Errorf("seed taskboard: %w", err)
	}

	const projectID = int64(100)
	openTasks, err := findOpenTasks(ctx, client, projectID)
	if err != nil {
		return fmt.Errorf("find open tasks: %w", err)
	}
	_, err = fmt.Fprintln(output, "Open tasks for Website refresh:")
	if err != nil {
		return fmt.Errorf("write heading: %w", err)
	}
	for _, task := range openTasks {
		if _, err := fmt.Fprintf(output, "- P%d %s\n", task.Priority, task.Title); err != nil {
			return fmt.Errorf("write task: %w", err)
		}
	}
	return nil
}

func seed(ctx context.Context, client rasql.Client) error {
	for _, member := range []Member{
		{ID: 1, Name: "Ada Lovelace", Email: "ada@example.com"},
		{ID: 2, Name: "Grace Hopper", Email: "grace@example.com"},
	} {
		if _, err := rasql.Insert(ctx, client, members, member); err != nil {
			return fmt.Errorf("insert member %q: %w", member.Email, err)
		}
	}

	project := Project{ID: 100, OwnerID: 1, Name: "Website refresh"}
	if _, err := rasql.Insert(ctx, client, projects, project); err != nil {
		return fmt.Errorf("insert project: %w", err)
	}

	for _, task := range []Task{
		{ID: 1, ProjectID: project.ID, AssigneeID: 1, Title: "Draft rollout plan", Status: "todo", Priority: 1},
		{ID: 2, ProjectID: project.ID, AssigneeID: 2, Title: "Review onboarding emails", Status: "todo", Priority: 2},
		{ID: 3, ProjectID: project.ID, AssigneeID: 1, Title: "Archive old mockups", Status: "done", Priority: 3},
	} {
		if _, err := rasql.Insert(ctx, client, tasks, task); err != nil {
			return fmt.Errorf("insert task %q: %w", task.Title, err)
		}
	}

	started := Task{
		ID:         1,
		ProjectID:  project.ID,
		AssigneeID: 1,
		Title:      "Draft rollout plan",
		Status:     "in_progress",
		Priority:   1,
	}
	if _, err := rasql.Update(ctx, client, tasks, started); err != nil {
		return fmt.Errorf("start task: %w", err)
	}
	return nil
}

func findOpenTasks(ctx context.Context, client rasql.Client, projectID int64) ([]taskSummary, error) {
	rows, err := rasql.DecodeFrom[taskSummary](client, tasks).
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
		Query(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]taskSummary, 0)
	for task, err := range rows {
		if err != nil {
			return nil, err
		}
		result = append(result, task)
	}
	return result, nil
}
