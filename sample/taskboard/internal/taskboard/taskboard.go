// Package taskboard holds the view model the Taskboard page is drawn from.
// It knows what the page shows and nothing about HTTP or SQL.
package taskboard

import (
	"time"

	"example.com/taskboard/internal/store"
)

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

// Page is everything one drawing of the page needs.
type Page struct {
	Groups   []Group
	Overdue  int64
	Projects []Choice
	Members  []Choice
}

// GroupByProject folds rows into one Group per project. It relies on the
// rows arriving in project order, which is the order
// store.Repository.OpenTasks returns them in, so it starts a new group
// every time the project changes and never revisits a finished one.
func GroupByProject(rows []store.OpenTask) []Group {
	groups := make([]Group, 0, len(rows))
	for _, row := range rows {
		if len(groups) == 0 || groups[len(groups)-1].ProjectID != row.ProjectID {
			groups = append(groups, Group{ProjectID: row.ProjectID, ProjectName: row.ProjectName})
		}
		group := &groups[len(groups)-1]
		group.Tasks = append(group.Tasks, Task{
			ID:       row.TaskID,
			Title:    row.Title,
			Assignee: assigneeText(row.AssigneeName),
			DueOn:    dueText(row.DueOn),
		})
	}
	return groups
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
