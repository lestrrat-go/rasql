// Command gen builds the Taskboard store from the SQLite migrations.
//
// The generator owns the database setup and schema selection, so the checked-in
// program is the complete source of truth for regenerating internal/store.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/internal/modroot"
	"github.com/lestrrat-go/rasql/migrate"

	_ "modernc.org/sqlite"
)

//go:generate go run .

const (
	migrationsDirectory = "migrations/sqlite"
	databaseFile        = "internal/store/.taskboard-schema.db"
	storeDirectory      = "internal/store"
)

func main() {
	check := flag.Bool("check", false, "report whether the generated store is current instead of writing it")
	flag.Parse()
	if err := run(context.Background(), *check); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, check bool) error {
	root, err := modroot.FromWorkingDirectory()
	if err != nil {
		return err
	}
	if root == "" {
		return errors.New("find taskboard module root: no go.mod found")
	}

	migrations, err := migrationdir.Load(filepath.Join(root, migrationsDirectory))
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}

	database, err := sql.Open("sqlite", filepath.Join(root, databaseFile))
	if err != nil {
		return fmt.Errorf("open schema database: %w", err)
	}
	database.SetMaxOpenConns(1)
	defer func() { _ = database.Close() }()

	d := dialect.SQLite()
	runner, err := migrate.New(database, d)
	if err != nil {
		return fmt.Errorf("create migration runner: %w", err)
	}
	if err := runner.Apply(ctx, migrations...); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	tables, err := catalog.FromDatabase(ctx, database, catalog.Options{Dialect: d})
	if err != nil {
		return fmt.Errorf("inspect schema: %w", err)
	}

	store := generate.Store{
		Package: "store",
		Root:    root,
		Dir:     storeDirectory,
		Tables:  tables,
		Prune:   true,
	}
	if check {
		return store.Check()
	}
	return store.Write()
}
