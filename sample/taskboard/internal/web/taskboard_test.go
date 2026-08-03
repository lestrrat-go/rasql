package web_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/sample/taskboard/internal/taskboard"
	"github.com/lestrrat-go/rasql/sample/taskboard/internal/web"
)

func TestTaskboardHandlerRendersOpenTasks(t *testing.T) {
	reader := &fakeTaskReader{
		tasks: []taskboard.Summary{{Title: "Draft rollout plan", Priority: 1}},
	}
	handler := web.NewTaskboardHandler(reader)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("response = %d, want %d", response.Code, http.StatusOK)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want HTML", contentType)
	}
	if !strings.Contains(response.Body.String(), "P1 Draft rollout plan") {
		t.Errorf("response body = %q, want task", response.Body.String())
	}
	if reader.limit != 51 || reader.offset != 0 {
		t.Errorf("page = limit %d, offset %d, want limit 51, offset 0", reader.limit, reader.offset)
	}
}

func TestTaskboardHandlerHidesStoreErrors(t *testing.T) {
	handler := web.NewTaskboardHandler(&fakeTaskReader{err: errors.New("database failed")})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("response = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if strings.Contains(response.Body.String(), "database failed") {
		t.Errorf("response exposes internal error: %q", response.Body.String())
	}
}

func TestTaskboardHandlerRequestsSelectedPage(t *testing.T) {
	reader := &fakeTaskReader{tasks: []taskboard.Summary{{Title: "Review onboarding emails", Priority: 2}}}
	handler := web.NewTaskboardHandler(reader)
	request := httptest.NewRequest(http.MethodGet, "/?page=2", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("response = %d, want %d", response.Code, http.StatusOK)
	}
	if reader.limit != 51 || reader.offset != 50 {
		t.Errorf("page = limit %d, offset %d, want limit 51, offset 50", reader.limit, reader.offset)
	}
}

func TestTaskboardHandlerHidesNextOnFinalFullPage(t *testing.T) {
	reader := &fakeTaskReader{tasks: make([]taskboard.Summary, 50)}
	handler := web.NewTaskboardHandler(reader)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("response = %d, want %d", response.Code, http.StatusOK)
	}
	if reader.limit != 51 {
		t.Errorf("limit = %d, want 51", reader.limit)
	}
	if strings.Contains(response.Body.String(), ">Next</a>") {
		t.Errorf("response body = %q, want no next page", response.Body.String())
	}
}

func TestTaskboardHandlerShowsNextForFollowingPage(t *testing.T) {
	tasks := append(make([]taskboard.Summary, 50), taskboard.Summary{Title: "Following task"})
	reader := &fakeTaskReader{tasks: tasks}
	handler := web.NewTaskboardHandler(reader)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("response = %d, want %d", response.Code, http.StatusOK)
	}
	if !strings.Contains(response.Body.String(), ">Next</a>") {
		t.Errorf("response body = %q, want next page", response.Body.String())
	}
	if strings.Count(response.Body.String(), "<li>") != 50 {
		t.Errorf("response body = %q, want 50 tasks", response.Body.String())
	}
	if strings.Contains(response.Body.String(), "Following task") {
		t.Errorf("response body = %q, rendered task from following page", response.Body.String())
	}
}

func TestTaskboardHandlerHidesNextOnMaximumPage(t *testing.T) {
	tasks := append(make([]taskboard.Summary, 50), taskboard.Summary{Title: "Following task"})
	reader := &fakeTaskReader{tasks: tasks}
	handler := web.NewTaskboardHandler(reader)
	maxOffset := int(^uint(0) >> 1)
	maximumPage := maxOffset/50 + 1
	request := httptest.NewRequest(http.MethodGet, "/?page="+strconv.Itoa(maximumPage), nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("response = %d, want %d", response.Code, http.StatusOK)
	}
	if strings.Contains(response.Body.String(), ">Next</a>") {
		t.Errorf("response body = %q, want no next page", response.Body.String())
	}
}

func TestTaskboardHandlerRejectsInvalidPage(t *testing.T) {
	reader := &fakeTaskReader{}
	handler := web.NewTaskboardHandler(reader)
	request := httptest.NewRequest(http.MethodGet, "/?page=0", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("response = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if reader.limit != 0 || reader.offset != 0 {
		t.Errorf("reader was called with limit %d and offset %d", reader.limit, reader.offset)
	}
}

type fakeTaskReader struct {
	tasks  []taskboard.Summary
	err    error
	limit  int
	offset int
}

func (reader *fakeTaskReader) OpenTasks(_ context.Context, _ int64, limit int, offset int) ([]taskboard.Summary, error) {
	reader.limit = limit
	reader.offset = offset
	return reader.tasks, reader.err
}
