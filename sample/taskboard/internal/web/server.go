package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// ErrServerClosed reports the expected result of context cancellation.
var ErrServerClosed = errors.New("taskboard: server closed")

// Controller reports the lifecycle of a running Server.
type Controller struct {
	done    chan struct{}
	address string

	mu  sync.RWMutex
	err error
}

// Done closes after the server has stopped.
func (controller *Controller) Done() <-chan struct{} {
	return controller.done
}

// Err returns the terminal server error after Done closes.
func (controller *Controller) Err() error {
	controller.mu.RLock()
	defer controller.mu.RUnlock()
	return controller.err
}

// Wait blocks until the server stops and returns its terminal error.
func (controller *Controller) Wait() error {
	<-controller.Done()
	return controller.Err()
}

// Addr returns the listener address assigned by the operating system.
func (controller *Controller) Addr() string {
	return controller.address
}

func (controller *Controller) setErr(err error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	controller.err = err
}

// Server is the Taskboard HTTP server configuration.
type Server struct {
	address string
	handler http.Handler
}

// NewServer creates an HTTP server that listens on address.
func NewServer(address string, handler http.Handler) Server {
	return Server{address: address, handler: handler}
}

// Run binds the listener and serves requests until ctx is cancelled.
func (server Server) Run(ctx context.Context) (*Controller, error) {
	listener, err := net.Listen("tcp", server.address)
	if err != nil {
		return nil, err
	}

	httpServer := newHTTPServer(server.handler)
	controller := &Controller{
		done:    make(chan struct{}),
		address: listener.Addr().String(),
	}
	go func() {
		defer close(controller.done)
		serve := make(chan error, 1)
		go func() {
			serve <- httpServer.Serve(listener)
		}()

		select {
		case err := <-serve:
			controller.setErr(serverError(err))
		case <-ctx.Done():
			_ = httpServer.Close()
			controller.setErr(serverError(<-serve))
		}
	}()
	return controller, nil
}

func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       time.Minute,
	}
}

func serverError(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return ErrServerClosed
	}
	return err
}
