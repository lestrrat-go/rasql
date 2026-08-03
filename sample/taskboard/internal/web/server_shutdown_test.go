package web

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestServerRunDrainsActiveRequestsOnCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		close(started)
		<-release
		response.WriteHeader(http.StatusNoContent)
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	controller, err := NewServer("127.0.0.1:0", handler).Run(ctx)
	if err != nil {
		t.Fatalf("start server: %s", err)
	}
	responseDone := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + controller.Addr())
		if err == nil {
			err = response.Body.Close()
		}
		responseDone <- err
	}()
	<-started
	cancel()

	select {
	case <-controller.Done():
		t.Fatal("server stopped before the active request completed")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	if err := <-responseDone; err != nil {
		t.Fatalf("complete request: %s", err)
	}
	if err := controller.Wait(); !errors.Is(err, ErrServerClosed) {
		t.Errorf("Wait() error = %v, want ErrServerClosed", err)
	}
}
