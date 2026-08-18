// Command gen builds the Taskboard store from a migrated SQLite database.
//
// It is the program a project owns: it names the package, the output
// directory, and the table selection, and nothing else. Applying the
// migrations that produce the database it reads is the job of
// scripts/generate.sh, which sets TASKBOARD_SCHEMA_DSN and runs this.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"

	_ "modernc.org/sqlite"
)

//go:generate ../scripts/generate.sh

// inspectionTimeout bounds the live-schema read.
const inspectionTimeout = 30 * time.Second

// storeDirectory is where the generated package is written, relative to the
// module root generate.Store finds for itself.
const storeDirectory = "internal/store"

func main() {
	check := flag.Bool("check", false, "report whether the generated store is current instead of writing it")
	flag.Parse()
	if err := run(context.Background(), *check); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, check bool) error {
	ctx, cancel := context.WithTimeout(ctx, inspectionTimeout)
	defer cancel()

	dsn := os.Getenv("TASKBOARD_SCHEMA_DSN")
	if dsn == "" {
		return errors.New("set TASKBOARD_SCHEMA_DSN to a migrated database, or run scripts/generate.sh, which does it for you")
	}
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open schema database: %w", err)
	}
	database.SetMaxOpenConns(1)
	defer func() { _ = database.Close() }()

	d := dialect.SQLite()
	tables, err := catalog.FromDatabase(ctx, database, catalog.Options{Dialect: d})
	if err != nil {
		return fmt.Errorf("inspect schema: %w", err)
	}

	store := generate.Store{
		Package: "store",
		Dir:     storeDirectory,
		Tables:  tables,
		Dialect: d,
		Prune:   true,
	}
	if check {
		return store.Check()
	}
	return store.Write()
}
