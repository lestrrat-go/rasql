package web

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"example.com/taskboard/internal/taskboard"
)

func TestTaskboardHandlerLogsStoreErrors(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	handler, err := newTaskboardHandler(failingTaskReader{}, logger)
	require.NoError(t, err)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("response = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(output.String(), "read open tasks") {
		t.Errorf("log output = %q, want operation", output.String())
	}
	if !strings.Contains(output.String(), "database failed") {
		t.Errorf("log output = %q, want store error", output.String())
	}
}

type failingTaskReader struct{}

func (failingTaskReader) OpenTasks(context.Context, int64, int, int) ([]taskboard.Summary, error) {
	return nil, errors.New("database failed")
}
