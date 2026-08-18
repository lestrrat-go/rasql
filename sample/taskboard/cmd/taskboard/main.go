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
	"example.com/taskboard/internal/store"
	"example.com/taskboard/internal/web"
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
	database, err := sql.Open("sqlite", databaseDSN())
	if err != nil {
		return web.Server{}, nil, fmt.Errorf("open SQLite database: %w", err)
	}
	database.SetMaxOpenConns(1)

	// SQLite enables foreign keys per connection, so keep its configuration on
	// the application's single connection.
	if _, err := database.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		_ = database.Close()
		return web.Server{}, nil, fmt.Errorf("enable SQLite foreign keys: %w", err)
	}
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		_ = database.Close()
		return web.Server{}, nil, fmt.Errorf("create rasql db: %w", err)
	}
	repository := store.New(db)
	if err := repository.SeedDemo(ctx); err != nil {
		_ = database.Close()
		return web.Server{}, nil, fmt.Errorf("seed taskboard: %w", err)
	}
	handler, err := web.NewTaskboardHandler(repository)
	if err != nil {
		_ = database.Close()
		return web.Server{}, nil, fmt.Errorf("create taskboard handler: %w", err)
	}
	return web.NewServer(listenAddress(), handler), database, nil
}

func listenAddress() string {
	if address := os.Getenv("TASKBOARD_ADDR"); address != "" {
		return address
	}
	return "127.0.0.1:8080"
}

func databaseDSN() string {
	if dsn := os.Getenv("TASKBOARD_DSN"); dsn != "" {
		return dsn
	}
	return "taskboard.db"
}
