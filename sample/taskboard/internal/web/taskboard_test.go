package web_test

import (
	"context"
	"html"
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
	"github.com/lestrrat-go/rasql"
)

// fakeRepository stands in for store.Repository. The handler names what it
// needs as two interfaces, so this needs no database and no rasql.
type fakeRepository struct {
	projects    store.OpenProjectsPage
	pageRequest rasql.PageRequest
	overdue     int64
	addedTitle  string
	addedOwner  *int64
	closedTask  int64
	closeCalled bool
}

func (f *fakeRepository) OpenProjects(_ context.Context, request rasql.PageRequest) (store.OpenProjectsPage, error) {
	f.pageRequest = request
	return f.projects, nil
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
		projects: store.OpenProjectsPage{Values: []store.OpenProject{{
			Row: store.ProjectsRow{ID: 1, Name: "Website refresh"},
			Tasks: rasql.LoadedMany[store.OpenTask]{Loaded: true, Values: []store.OpenTask{
				{Row: store.TasksRow{ID: 7, Title: "Draft the rollout plan"}, Assignee: rasql.LoadedOne[store.MembersRow]{Loaded: true, Present: true, Value: &store.MembersRow{Name: ada}}},
				{Row: store.TasksRow{ID: 8, Title: "Pick a heading typeface"}, Assignee: rasql.LoadedOne[store.MembersRow]{Loaded: true}},
			}},
		}}},
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

func TestShowPagePassesCursorRequest(t *testing.T) {
	repository := &fakeRepository{}
	request := httptest.NewRequest(http.MethodGet, "/?after=next-token&limit=5", nil)
	recorder := httptest.NewRecorder()
	newTestHandler(repository).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET / returned %d, want 200", recorder.Code)
	}
	if repository.pageRequest.After != rasql.Cursor("next-token") || repository.pageRequest.Limit != 5 {
		t.Fatalf("OpenProjects got %#v, want cursor and limit", repository.pageRequest)
	}
}

func TestShowPageNextLinkPreservesAcceptedLimit(t *testing.T) {
	repository := &fakeRepository{projects: store.OpenProjectsPage{HasMore: true, Next: "next-token"}}
	first := httptest.NewRecorder()
	newTestHandler(repository).ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/?limit=5", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first GET / returned %d, want 200", first.Code)
	}
	const prefix = `href="`
	start := strings.Index(first.Body.String(), prefix)
	if start < 0 {
		t.Fatalf("first page does not contain a next link: %s", first.Body.String())
	}
	start += len(prefix)
	end := strings.Index(first.Body.String()[start:], `"`)
	if end < 0 {
		t.Fatalf("first page has an unterminated next link: %s", first.Body.String())
	}
	next := first.Body.String()[start : start+end]
	if next != "/?after=next-token&amp;limit=5" {
		t.Fatalf("next link = %q, want accepted limit", next)
	}
	nextURL := html.UnescapeString(next)

	second := httptest.NewRecorder()
	newTestHandler(repository).ServeHTTP(second, httptest.NewRequest(http.MethodGet, nextURL, nil))
	if second.Code != http.StatusOK {
		t.Fatalf("second GET / returned %d, want 200", second.Code)
	}
	if repository.pageRequest.After != rasql.Cursor("next-token") || repository.pageRequest.Limit != 5 {
		t.Fatalf("followed next link sent %#v, want cursor and limit 5", repository.pageRequest)
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
