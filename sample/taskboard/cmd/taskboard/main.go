package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/sample/taskboard/internal/store"
	"github.com/lestrrat-go/rasql/sample/taskboard/internal/web"
	_ "modernc.org/sqlite"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server, database, err := newServer(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "taskboard: %s\n", err)
		os.Exit(1)
	}
	defer database.Close()

	controller, err := server.Run(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "taskboard: %s\n", err)
		os.Exit(1)
	}
	fmt.Printf("Taskboard is listening at http://%s\n", controller.Addr())
	if err := controller.Wait(); err != nil && !errors.Is(err, web.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "taskboard: %s\n", err)
		os.Exit(1)
	}
}

func newServer(ctx context.Context) (web.Server, *sql.DB, error) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return web.Server{}, nil, fmt.Errorf("open SQLite database: %w", err)
	}
	database.SetMaxOpenConns(1)

	// SQLite enables foreign keys per connection, so keep its configuration and
	// the in-memory database on this single connection.
	if _, err := database.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		_ = database.Close()
		return web.Server{}, nil, fmt.Errorf("enable SQLite foreign keys: %w", err)
	}
	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		_ = database.Close()
		return web.Server{}, nil, fmt.Errorf("create rasql client: %w", err)
	}
	if err := store.CreateSchema(ctx, client); err != nil {
		_ = database.Close()
		return web.Server{}, nil, fmt.Errorf("create taskboard schema: %w", err)
	}
	repository := store.New(client)
	if err := repository.SeedDemo(ctx); err != nil {
		_ = database.Close()
		return web.Server{}, nil, fmt.Errorf("seed taskboard: %w", err)
	}
	return web.NewServer(listenAddress(), web.NewTaskboardHandler(repository)), database, nil
}

func listenAddress() string {
	if address := os.Getenv("TASKBOARD_ADDR"); address != "" {
		return address
	}
	return "127.0.0.1:8080"
}
