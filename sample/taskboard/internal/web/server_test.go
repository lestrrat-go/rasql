package web

import (
	"net/http"
	"testing"
	"time"
)

func TestNewHTTPServerSetsConnectionTimeouts(t *testing.T) {
	server := newHTTPServer(http.NotFoundHandler())

	if server.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("ReadHeaderTimeout = %s, want %s", server.ReadHeaderTimeout, 5*time.Second)
	}
	if server.ReadTimeout != 10*time.Second {
		t.Errorf("ReadTimeout = %s, want %s", server.ReadTimeout, 10*time.Second)
	}
	if server.IdleTimeout != time.Minute {
		t.Errorf("IdleTimeout = %s, want %s", server.IdleTimeout, time.Minute)
	}
}
