// Command gen intentionally exercises the supported compatibility
// `rasqlgen bootstrap` workflow. New projects should normally own the
// generator program created by `rasqlgen init`; this example instead builds
// the throwaway SQLite database used by the bootstrap documentation, runs
// `rasqlgen bootstrap` against it, and writes the description package into
// internal/tables so the checked-in files came from exactly this run.
//
// The generate directive lives in the package next door, so this program
// runs with its working directory at examples/bootstrapsource.
package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	_ "modernc.org/sqlite"
)

const (
	outputDirectory = "internal/tables"
	schemaPath      = "schema.sql"
)

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
	// The schema lives in schema.sql rather than in this file so the
	// documentation can show the exact DDL the checked-in description
	// package was generated from, instead of a retyped copy of it.
	schemaSQL, err := os.ReadFile(schemaPath)
	if err != nil {
		_ = database.Close()
		return fmt.Errorf("read %s: %w", schemaPath, err)
	}
	for _, statement := range strings.Split(string(schemaSQL), ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := database.Exec(statement); err != nil {
			_ = database.Close()
			return fmt.Errorf("apply %s: %w", schemaPath, err)
		}
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
