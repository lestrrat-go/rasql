package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/sample/taskboard/internal/web"
)

func TestNewServerServesTaskboard(t *testing.T) {
	t.Setenv("TASKBOARD_ADDR", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	server, database, err := newServer(ctx)
	if err != nil {
		t.Fatalf("configure taskboard server: %s", err)
	}
	defer database.Close()

	controller, err := server.Run(ctx)
	if err != nil {
		t.Fatalf("start taskboard server: %s", err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+controller.Addr()+"/", nil)
	if err != nil {
		t.Fatalf("create taskboard request: %s", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("request taskboard page: %s", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("taskboard response = %d, want %d", response.StatusCode, http.StatusOK)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read taskboard page: %s", err)
	}
	const expected = "Draft rollout plan"
	if !strings.Contains(string(body), expected) {
		t.Errorf("taskboard page does not contain %q: %q", expected, body)
	}

	cancel()
	if err := controller.Wait(); !errors.Is(err, web.ErrServerClosed) {
		t.Errorf("wait for taskboard server = %v, want %v", err, web.ErrServerClosed)
	}
}
