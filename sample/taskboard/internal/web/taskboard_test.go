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
	handler := web.NewTaskboardHandler(fakeTaskReader{
		tasks: []taskboard.Summary{{Title: "Draft rollout plan", Priority: 1}},
	})
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
}

func TestTaskboardHandlerHidesStoreErrors(t *testing.T) {
	handler := web.NewTaskboardHandler(fakeTaskReader{err: errors.New("database failed")})
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

type fakeTaskReader struct {
	tasks []taskboard.Summary
	err   error
}

func (reader fakeTaskReader) OpenTasks(_ context.Context, _ int64) ([]taskboard.Summary, error) {
	return reader.tasks, reader.err
}
