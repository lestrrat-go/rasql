// Package diff generates reviewed SQL migrations from desired schema sources.
package diff

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
)

// Source is one SQL file in a desired schema tree.
type Source struct {
	Path string
	SQL  sqltext.Text
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

// LiveAnalyzer supplies the dialect-specific boundary used by diff-live.
// LiveSources converts one inspected table into desired-schema sources, and
// ValidateLivePlan checks that generated statements stay within that table.
type LiveAnalyzer interface {
	Analyzer
	LiveSources(schema.TableDef) ([]Source, error)
	ValidateLivePlan(Plan, string) error
}

// Schema groups the tables and indexes in one parsed schema. The map keys are
// canonicalized by the dialect package that parsed the schema.
type Schema[Table, Index any] struct {
	Tables  map[string]Table
	Indexes map[string]Index
}

// SchemaEntry identifies one schema object by its dialect-canonical key.
type SchemaEntry[T any] struct {
	Key   string
	Value T
}

// SchemaPair contains the baseline and target versions of one schema object.
type SchemaPair[T any] struct {
	Key      string
	Baseline T
	Target   T
	Equal    bool
}

// SchemaChanges describes added, removed, and matched objects in one category.
type SchemaChanges[T any] struct {
	Added   []SchemaEntry[T]
	Removed []SchemaEntry[T]
	Matched []SchemaPair[T]
}

// SchemaComparison is the canonical comparison of two parsed schemas.
type SchemaComparison[Table, Index any] struct {
	Tables  SchemaChanges[Table]
	Indexes SchemaChanges[Index]
}

// CompareSchemas compares schema objects by their dialect-canonical keys.
// Dialects supply equality functions because identifier folding and metadata
// normalization are database rules, not shared migration rules.
func CompareSchemas[Table, Index any](baseline, target Schema[Table, Index], tableEqual func(Table, Table) bool, indexEqual func(Index, Index) bool) SchemaComparison[Table, Index] {
	return SchemaComparison[Table, Index]{
		Tables:  compareSchemaObjects(baseline.Tables, target.Tables, tableEqual),
		Indexes: compareSchemaObjects(baseline.Indexes, target.Indexes, indexEqual),
	}
}

func compareSchemaObjects[T any](baseline, target map[string]T, equal func(T, T) bool) SchemaChanges[T] {
	changes := SchemaChanges[T]{
		Added:   make([]SchemaEntry[T], 0),
		Removed: make([]SchemaEntry[T], 0),
		Matched: make([]SchemaPair[T], 0),
	}
	keys := make([]string, 0, len(target))
	for key := range target {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		targetValue := target[key]
		baselineValue, exists := baseline[key]
		if !exists {
			changes.Added = append(changes.Added, SchemaEntry[T]{Key: key, Value: targetValue})
			continue
		}
		changes.Matched = append(changes.Matched, SchemaPair[T]{
			Key:      key,
			Baseline: baselineValue,
			Target:   targetValue,
			Equal:    equal == nil || equal(baselineValue, targetValue),
		})
	}
	keys = keys[:0]
	for key := range baseline {
		if _, exists := target[key]; !exists {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		changes.Removed = append(changes.Removed, SchemaEntry[T]{Key: key, Value: baseline[key]})
	}
	return changes
}

// Plan is a reviewed set of SQL sources generated for one migration.
type Plan struct {
	Dialect    string
	Statements []PlannedStatement
}

// PlannedStatement is one generated native SQL source file.
type PlannedStatement struct {
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
	sources := make(map[string]int, len(p.Statements))
	for index, statement := range p.Statements {
		if statement.Source == "" || filepath.Base(statement.Source) != statement.Source || strings.HasPrefix(statement.Source, ".") || filepath.Ext(statement.Source) != ".sql" {
			return fmt.Errorf("migrate diff: generated SQL source %d is invalid", index+1)
		}
		if strings.TrimSpace(statement.SQL) == "" {
			return fmt.Errorf("migrate diff: generated SQL source %q is empty", statement.Source)
		}
		if previous, exists := sources[statement.Source]; exists {
			return fmt.Errorf("migrate diff: duplicate generated SQL source %q for %q and %q", statement.Source, p.Statements[previous].Summary, statement.Summary)
		}
		sources[statement.Source] = index
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
