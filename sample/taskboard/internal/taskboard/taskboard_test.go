package taskboard_test

import (
	"testing"
	"time"

	"example.com/taskboard/internal/store"
	"example.com/taskboard/internal/taskboard"
	"github.com/lestrrat-go/rasql"
)

func TestOpenProjectGroupsConvertsLoadedGraph(t *testing.T) {
	due := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	ada := store.MembersRow{ID: 7, Name: "Ada Lovelace"}
	websiteTasks := rasql.LoadedMany[store.OpenTask]{Loaded: true}
	websiteTasks.Values = append(websiteTasks.Values, store.OpenTask{Row: store.TasksRow{ID: 1, Title: "Draft the rollout plan"}, Assignee: rasql.LoadedOne[store.MembersRow]{Loaded: true, Present: true, Value: &ada}})
	websiteTasks.Values = append(websiteTasks.Values, store.OpenTask{Row: store.TasksRow{ID: 2, Title: "Pick a heading typeface"}, Assignee: rasql.LoadedOne[store.MembersRow]{Loaded: true}})
	billingTasks := rasql.LoadedMany[store.OpenTask]{Loaded: true}
	billingTasks.Values = append(billingTasks.Values, store.OpenTask{Row: store.TasksRow{ID: 3, Title: "Reconcile March invoices", DueOn: rasql.Nullable[time.Time]{Value: due, Valid: true}}, Assignee: rasql.LoadedOne[store.MembersRow]{Loaded: true, Present: true, Value: &ada}})
	groups, err := taskboard.OpenProjectGroups([]store.OpenProject{
		{Row: store.ProjectsRow{ID: 1, Name: "Website refresh"}, Tasks: websiteTasks},
		{Row: store.ProjectsRow{ID: 2, Name: "Billing cleanup"}, Tasks: billingTasks},
	})
	if err != nil {
		t.Fatalf("OpenProjectGroups returned an error: %s", err)
	}

	if len(groups) != 2 {
		t.Fatalf("OpenProjectGroups returned %d groups, want 2", len(groups))
	}
	if groups[0].ProjectName != "Website refresh" || len(groups[0].Tasks) != 2 {
		t.Errorf("first group is %q with %d tasks, want Website refresh with 2", groups[0].ProjectName, len(groups[0].Tasks))
	}
	if got := groups[0].Tasks[1].Assignee; got != taskboard.Unassigned {
		t.Errorf("task with no owner shows %q, want %q", got, taskboard.Unassigned)
	}
	if got := groups[0].Tasks[0].DueOn; got != "" {
		t.Errorf("task with no due date shows %q, want an empty string", got)
	}
	if got := groups[1].Tasks[0].DueOn; got != "2026-08-25" {
		t.Errorf("due date shows %q, want 2026-08-25", got)
	}
}

func TestOpenProjectGroupsPreservesProjectRows(t *testing.T) {
	groups, err := taskboard.OpenProjectGroups([]store.OpenProject{
		{Row: store.ProjectsRow{ID: 1, Name: "A"}, Tasks: rasql.LoadedMany[store.OpenTask]{Loaded: true}},
		{Row: store.ProjectsRow{ID: 2, Name: "B"}, Tasks: rasql.LoadedMany[store.OpenTask]{Loaded: true}},
		{Row: store.ProjectsRow{ID: 1, Name: "A"}, Tasks: rasql.LoadedMany[store.OpenTask]{Loaded: true}},
	})
	if err != nil {
		t.Fatalf("OpenProjectGroups returned an error: %s", err)
	}
	if len(groups) != 3 {
		t.Fatalf("OpenProjectGroups returned %d groups, want 3", len(groups))
	}
}

func TestOpenProjectGroupsOnNoRows(t *testing.T) {
	if groups, err := taskboard.OpenProjectGroups(nil); err != nil || len(groups) != 0 {
		t.Errorf("OpenProjectGroups(nil) returned %d groups and %v, want 0 and no error", len(groups), err)
	}
}

func TestOpenProjectGroupsKeepsLoadedEmptyAndUnloadedState(t *testing.T) {
	groups, err := taskboard.OpenProjectGroups([]store.OpenProject{
		{Row: store.ProjectsRow{ID: 1, Name: "Empty"}, Tasks: rasql.LoadedMany[store.OpenTask]{Loaded: true, Values: []store.OpenTask{}}},
		{Row: store.ProjectsRow{ID: 2, Name: "Unloaded"}},
	})
	if err == nil {
		t.Fatal("OpenProjectGroups accepted an unloaded task collection")
	}
	if groups != nil {
		t.Fatalf("OpenProjectGroups returned groups with unloaded state: %#v", groups)
	}

	groups, err = taskboard.OpenProjectGroups([]store.OpenProject{{
		Row:   store.ProjectsRow{ID: 1, Name: "Empty"},
		Tasks: rasql.LoadedMany[store.OpenTask]{Loaded: true, Values: []store.OpenTask{}},
	}})
	if err != nil {
		t.Fatalf("OpenProjectGroups rejected loaded empty state: %s", err)
	}
	if groups[0].Tasks == nil {
		t.Fatal("loaded empty task collection became nil")
	}
}

func TestOpenProjectGroupsRejectsUnloadedAssignee(t *testing.T) {
	projects := []store.OpenProject{{
		Row:   store.ProjectsRow{ID: 1, Name: "Project"},
		Tasks: rasql.LoadedMany[store.OpenTask]{Loaded: true, Values: []store.OpenTask{{Row: store.TasksRow{ID: 7}}}},
	}}
	groups, err := taskboard.OpenProjectGroups(projects)
	if err == nil || groups != nil {
		t.Fatalf("OpenProjectGroups returned groups=%#v, err=%v for unloaded assignee", groups, err)
	}
}
