// Command taskboard serves the Taskboard page over HTTP.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"example.com/taskboard/internal/store"
	"example.com/taskboard/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "taskboard: %s\n", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	dsn := os.Getenv("TASKBOARD_DSN")
	if dsn == "" {
		return errors.New("set TASKBOARD_DSN to the taskboard connection string")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse TASKBOARD_DSN: %w", err)
	}
	database := stdlib.OpenDB(*config)
	defer func() { _ = database.Close() }()

	// A rasql.DB pairs the handle with the dialect used to render SQL.
	db, err := rasql.New(database, dialect.PostgreSQL())
	if err != nil {
		return fmt.Errorf("create the rasql db: %w", err)
	}

	address := os.Getenv("TASKBOARD_ADDR")
	if address == "" {
		address = "127.0.0.1:8080"
	}
	repository := store.New(db)
	handler := web.NewHandler(repository, repository, logger)
	server := &http.Server{
		Addr:              address,
		Handler:           handler.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listening := make(chan error, 1)
	go func() {
		logger.Info("taskboard is listening", slog.String("address", address))
		listening <- server.ListenAndServe()
	}()

	select {
	case err := <-listening:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down: %w", err)
	}
	logger.Info("taskboard stopped")
	return nil
}
