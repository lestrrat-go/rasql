package taskboard_test

import (
	"testing"
	"time"

	"example.com/taskboard/internal/store"
	"example.com/taskboard/internal/taskboard"
)

func ptr[T any](value T) *T { return &value }

func TestGroupByProject(t *testing.T) {
	due := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	ada := "Ada Lovelace"
	groups := taskboard.GroupByProject([]store.OpenTask{
		{ProjectID: 1, ProjectName: "Website refresh", TaskID: 1, Title: "Draft the rollout plan", AssigneeName: &ada},
		{ProjectID: 1, ProjectName: "Website refresh", TaskID: 2, Title: "Pick a heading typeface"},
		{ProjectID: 2, ProjectName: "Billing cleanup", TaskID: 3, Title: "Reconcile March invoices", AssigneeName: &ada, DueOn: ptr(due)},
	})

	if len(groups) != 2 {
		t.Fatalf("GroupByProject returned %d groups, want 2", len(groups))
	}
	if groups[0].ProjectName != "Website refresh" || len(groups[0].Tasks) != 2 {
		t.Errorf("first group is %q with %d tasks, want \"Website refresh\" with 2", groups[0].ProjectName, len(groups[0].Tasks))
	}
	if got := groups[0].Tasks[1].Assignee; got != taskboard.Unassigned {
		t.Errorf("task with no owner shows %q, want %q", got, taskboard.Unassigned)
	}
	if got := groups[0].Tasks[0].DueOn; got != "" {
		t.Errorf("task with no due date shows %q, want an empty string", got)
	}
	if got := groups[1].Tasks[0].DueOn; got != "2026-08-25" {
		t.Errorf("due date shows %q, want \"2026-08-25\"", got)
	}
}

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

func TestGroupByProjectOnNoRows(t *testing.T) {
	if groups := taskboard.GroupByProject(nil); len(groups) != 0 {
		t.Errorf("GroupByProject(nil) returned %d groups, want 0", len(groups))
	}
}
