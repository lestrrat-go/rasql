package web_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
	if reader.limit != 50 || reader.offset != 0 {
		t.Errorf("page = limit %d, offset %d, want limit 50, offset 0", reader.limit, reader.offset)
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
	if reader.limit != 50 || reader.offset != 50 {
		t.Errorf("page = limit %d, offset %d, want limit 50, offset 50", reader.limit, reader.offset)
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
