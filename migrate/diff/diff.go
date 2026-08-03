// Package diff generates reviewed SQL migrations from desired schema sources.
package diff

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Source is one SQL file in a desired schema tree.
type Source struct {
	Path string
	SQL  string
}

// Snapshot is a parsed desired schema owned by one Analyzer.
type Snapshot interface {
	Dialect() string
}

// Analyzer parses and compares desired schemas for one database dialect.
type Analyzer interface {
	Dialect() string
	Parse([]Source) (Snapshot, error)
	Diff(Snapshot, Snapshot) (Plan, error)
}

// Plan is a reviewed set of SQL sources generated for one migration.
type Plan struct {
	Dialect    string
	Statements []Statement
}

// Statement is one generated native SQL source file.
type Statement struct {
	Source  string
	SQL     string
	Summary string
}

// Empty reports whether a plan contains no generated SQL sources.
func (p Plan) Empty() bool {
	return len(p.Statements) == 0
}

// Validate reports whether p can be written as one migration directory.
func (p Plan) Validate() error {
	if p.Dialect == "" {
		return fmt.Errorf("migrate diff: plan dialect must not be empty")
	}
	if len(p.Statements) == 0 {
		return fmt.Errorf("migrate diff: plan has no SQL sources")
	}
	sources := make(map[string]struct{}, len(p.Statements))
	for index, statement := range p.Statements {
		if statement.Source == "" || filepath.Base(statement.Source) != statement.Source || strings.HasPrefix(statement.Source, ".") || filepath.Ext(statement.Source) != ".sql" {
			return fmt.Errorf("migrate diff: generated SQL source %d is invalid", index+1)
		}
		if strings.TrimSpace(statement.SQL) == "" {
			return fmt.Errorf("migrate diff: generated SQL source %q is empty", statement.Source)
		}
		if _, exists := sources[statement.Source]; exists {
			return fmt.Errorf("migrate diff: duplicate generated SQL source %q", statement.Source)
		}
		sources[statement.Source] = struct{}{}
	}
	return nil
}

// WriteMigration writes p into a new migration directory at directory.
func WriteMigration(directory string, p Plan) error {
	if directory == "" {
		return fmt.Errorf("migrate diff: output directory must not be empty")
	}
	if err := p.Validate(); err != nil {
		return err
	}
	parent := filepath.Dir(directory)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("migrate diff: create migration parent directory: %w", err)
	}
	temporary, err := os.MkdirTemp(parent, "."+filepath.Base(directory)+".tmp-")
	if err != nil {
		return fmt.Errorf("migrate diff: create temporary migration directory: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(temporary)
		}
	}()
	for _, statement := range p.Statements {
		path := filepath.Join(temporary, statement.Source)
		if err := os.WriteFile(path, []byte(statement.SQL), 0o600); err != nil {
			return fmt.Errorf("migrate diff: write generated SQL source %q: %w", statement.Source, err)
		}
	}
	if err := os.Rename(temporary, directory); err != nil {
		return fmt.Errorf("migrate diff: create migration directory %q: %w", directory, err)
	}
	committed = true
	return nil
}
