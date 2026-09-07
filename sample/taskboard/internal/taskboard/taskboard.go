// Package taskboard holds the view model the Taskboard page is drawn from.
// It knows what the page shows and nothing about HTTP or SQL.
package taskboard

import (
	"fmt"
	"time"

	"example.com/taskboard/internal/store"
	"github.com/lestrrat-go/rasql"
)

// BEGIN(task_text)

// Task is one open task as the page prints it.
type Task struct {
	ID       int64
	Title    string
	Assignee string
	DueOn    string
}

// Unassigned is what the page prints where an owner's name would go.
const Unassigned = "unassigned"

func assigneeText(loaded rasql.LoadedOne[store.MembersRow]) (string, error) {
	if !loaded.Loaded {
		return "", fmt.Errorf("assignee state is unloaded")
	}
	if !loaded.Present || loaded.Value == nil {
		return Unassigned, nil
	}
	return loaded.Value.Name, nil
}

func dueText(due rasql.Nullable[time.Time]) string {
	if !due.Valid {
		return ""
	}
	return due.Value.Format(time.DateOnly)
}

// END(task_text)

// Group is one project's block of open tasks.
type Group struct {
	ProjectID   int64
	ProjectName string
	Tasks       []Task
}

// Choice is one entry of the add-task form's project or member list.
type Choice struct {
	ID   int64
	Name string
}

// BEGIN(page)

// Page is everything one drawing of the page needs.
type Page struct {
	Groups   []Group
	Overdue  int64
	Projects []Choice
	Members  []Choice
	Limit    int
	Next     string
	HasMore  bool
}

// END(page)

// OpenProjectGroups converts loaded graph values into the page's project blocks.
func OpenProjectGroups(projects []store.OpenProject) ([]Group, error) {
	groups := make([]Group, 0, len(projects))
	for _, project := range projects {
		if !project.Tasks.Loaded {
			return nil, fmt.Errorf("project %d task state is unloaded", project.Row.ID)
		}
		group := Group{ProjectID: project.Row.ID, ProjectName: project.Row.Name}
		group.Tasks = make([]Task, 0, len(project.Tasks.Values))
		for _, task := range project.Tasks.Values {
			assignee, err := assigneeText(task.Assignee)
			if err != nil {
				return nil, fmt.Errorf("project %d task %d: %w", project.Row.ID, task.Row.ID, err)
			}
			group.Tasks = append(group.Tasks, Task{
				ID:       task.Row.ID,
				Title:    task.Row.Title,
				Assignee: assignee,
				DueOn:    dueText(task.Row.DueOn),
			})
		}
		groups = append(groups, group)
	}
	return groups, nil
}

// ProjectChoices turns project rows into the form's project list.
func ProjectChoices(rows []store.ProjectsRow) []Choice {
	choices := make([]Choice, 0, len(rows))
	for _, row := range rows {
		choices = append(choices, Choice{ID: row.ID, Name: row.Name})
	}
	return choices
}

// MemberChoices turns member rows into the form's member list.
func MemberChoices(rows []store.MembersRow) []Choice {
	choices := make([]Choice, 0, len(rows))
	for _, row := range rows {
		choices = append(choices, Choice{ID: row.ID, Name: row.Name})
	}
	return choices
}
