// Command gen is what the "own the program" alternative to `rasqlgen
// bootstrap` would look like, but its real job here is to build the
// throwaway SQLite database the documentation's bootstrap example is
// generated from: it creates a "users" table, runs `rasqlgen bootstrap`
// against it, and writes the description package into internal/tables, so
// the checked-in files there came from exactly this run rather than a
// hand-maintained copy.
//
// The generate directive lives in the package next door, so this program
// runs with its working directory at examples/bootstrapsource.
package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	_ "modernc.org/sqlite"
)

const outputDirectory = "internal/tables"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to bootstrap schema package: %s\n", err)
		os.Exit(1)
	}
}

func run() error {
	directory, err := os.MkdirTemp("", ".tmp-bootstrapsource-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(directory) }()

	databasePath := filepath.Join(directory, "schema.db")
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return fmt.Errorf("open throwaway database: %w", err)
	}
	if _, err := database.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)"); err != nil {
		_ = database.Close()
		return fmt.Errorf("create users table: %w", err)
	}
	if err := database.Close(); err != nil {
		return err
	}

	// WriteDescriptionPackage refuses a directory that already holds files,
	// including its own earlier output, so a rerun of this generator starts
	// from a clean directory rather than a stale one.
	if err := os.RemoveAll(outputDirectory); err != nil {
		return err
	}
	if err := os.MkdirAll(outputDirectory, 0o700); err != nil {
		return err
	}

	return rasqlgen.Run([]string{
		"bootstrap",
		"-dsn", databasePath,
		"-dialect", "sqlite",
		"-package", "tables",
		"-output", outputDirectory,
	}, os.Stderr)
}
