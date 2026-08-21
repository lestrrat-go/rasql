package web_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"example.com/taskboard/internal/store"
	"example.com/taskboard/internal/web"
)

// fakeRepository stands in for store.Repository. The handler names what it
// needs as two interfaces, so this needs no database and no rasql.
type fakeRepository struct {
	tasks       []store.OpenTask
	overdue     int64
	addedTitle  string
	addedOwner  *int64
	closedTask  int64
	closeCalled bool
}

func (f *fakeRepository) OpenTasks(context.Context) ([]store.OpenTask, error) {
	return f.tasks, nil
}

func (f *fakeRepository) AllProjects(context.Context) ([]store.ProjectsRow, error) {
	return []store.ProjectsRow{{ID: 1, Name: "Website refresh"}}, nil
}

func (f *fakeRepository) AllMembers(context.Context) ([]store.MembersRow, error) {
	return []store.MembersRow{{ID: 1, Name: "Ada Lovelace"}}, nil
}

func (f *fakeRepository) CountOverdue(context.Context, time.Time) (int64, error) {
	return f.overdue, nil
}

func (f *fakeRepository) AddTask(_ context.Context, _ int64, assigneeID *int64, title string) error {
	f.addedTitle = title
	f.addedOwner = assigneeID
	return nil
}

func (f *fakeRepository) CloseTask(_ context.Context, taskID int64) error {
	f.closedTask = taskID
	f.closeCalled = true
	return nil
}

func newTestHandler(repository *fakeRepository) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return web.NewHandler(repository, repository, logger).Routes()
}

func TestShowPage(t *testing.T) {
	ada := "Ada Lovelace"
	repository := &fakeRepository{
		tasks: []store.OpenTask{
			{ProjectID: 1, ProjectName: "Website refresh", TaskID: 7, Title: "Draft the rollout plan", AssigneeName: &ada},
			{ProjectID: 1, ProjectName: "Website refresh", TaskID: 8, Title: "Pick a heading typeface"},
		},
		overdue: 2,
	}
	recorder := httptest.NewRecorder()
	newTestHandler(repository).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET / returned %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{"Website refresh", "Draft the rollout plan", "unassigned", "Past their due date: 2"} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
}

// BEGIN(add_no_owner)
func TestAddTaskWithNoOwner(t *testing.T) {
	repository := &fakeRepository{}
	form := url.Values{"project_id": {"1"}, "assignee_id": {""}, "title": {"Find an owner"}}
	request := httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	newTestHandler(repository).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST /tasks returned %d, want 303", recorder.Code)
	}
	if repository.addedTitle != "Find an owner" {
		t.Errorf("AddTask got title %q, want \"Find an owner\"", repository.addedTitle)
	}
	if repository.addedOwner != nil {
		t.Errorf("AddTask got owner %v, want nil for an empty assignee_id", *repository.addedOwner)
	}
}

// END(add_no_owner)

func TestAddTaskRejectsABadProjectID(t *testing.T) {
	repository := &fakeRepository{}
	form := url.Values{"project_id": {"one"}, "title": {"x"}}
	request := httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	newTestHandler(repository).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST /tasks returned %d, want 400", recorder.Code)
	}
	if repository.addedTitle != "" {
		t.Error("AddTask ran for a request the handler should have rejected")
	}
}

func TestCloseTask(t *testing.T) {
	repository := &fakeRepository{}
	request := httptest.NewRequest(http.MethodPost, "/tasks/42/close", nil)
	recorder := httptest.NewRecorder()
	newTestHandler(repository).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST /tasks/42/close returned %d, want 303", recorder.Code)
	}
	if !repository.closeCalled || repository.closedTask != 42 {
		t.Errorf("CloseTask got %d (called: %t), want 42", repository.closedTask, repository.closeCalled)
	}
}
