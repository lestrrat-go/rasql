// Package postgresql compares PostgreSQL desired-schema sources.
package postgresql

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	pgquery "github.com/lestrrat-go/rasql-pg/query"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/ast"
	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/schema"
)

// Analyzer compares the supported PostgreSQL desired-schema subset.
type Analyzer struct{}

// New creates a PostgreSQL desired-schema analyzer.
func New() Analyzer {
	return Analyzer{}
}

// Dialect identifies PostgreSQL schema sources.
func (Analyzer) Dialect() string {
	return "postgresql"
}

// LiveSources converts one inspected PostgreSQL table into desired-schema sources.
func (Analyzer) LiveSources(table schema.Table) ([]diff.Source, error) {
	return diff.SourcesFromTable(dialect.PostgreSQL(), table)
}

// ValidateLivePlan ensures generated PostgreSQL statements stay within the selected table.
func (Analyzer) ValidateLivePlan(plan diff.Plan, tableName string) error {
	for _, statement := range plan.Statements {
		parsed, err := pgquery.ParseStatement(statement.SQL)
		if err != nil {
			return fmt.Errorf("validate live diff statement %q: %w", statement.Source, err)
		}
		switch parsed := parsed.(type) {
		case *pgquery.CreateTableStatement:
			if parsed.Name.String() != tableName {
				return fmt.Errorf("diff-live target contains table %q, but -table selects %q", parsed.Name.String(), tableName)
			}
		case *pgquery.CreateIndexStatement:
			if parsed.Table.String() != tableName {
				return fmt.Errorf("diff-live target contains an index for table %q, but -table selects %q", parsed.Table.String(), tableName)
			}
		}
	}
	return nil
}

// Parse reads CREATE TABLE and named CREATE INDEX statements from sources.
func (Analyzer) Parse(sources []diff.Source) (diff.Snapshot, error) {
	snapshot := &schemaSnapshot{
		tables:  make(map[string]tableDefinition),
		indexes: make(map[string]indexDefinition),
	}
	for _, source := range sources {
		parsed, err := pgquery.Parse(source.SQL)
		if err != nil {
			return nil, fmt.Errorf("postgresql schema source %q: %w", source.Path, err)
		}
		for index, statement := range parsed.Statements {
			switch statement := statement.(type) {
			case *pgquery.CreateTableStatement:
				if err := snapshot.addTable(source.Path, statement); err != nil {
					return nil, err
				}
			case *pgquery.CreateIndexStatement:
				if err := snapshot.addIndex(source.Path, statement); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("postgresql schema source %q statement %d must be CREATE TABLE or named CREATE INDEX, got %T", source.Path, index+1, statement)
			}
		}
	}
	if len(snapshot.tables) == 0 {
		return nil, fmt.Errorf("postgresql schema has no CREATE TABLE statements")
	}
	return snapshot, nil
}

// Diff returns safe, additive changes from from to to.
func (Analyzer) Diff(from diff.Snapshot, to diff.Snapshot) (diff.Plan, error) {
	baseline, ok := from.(*schemaSnapshot)
	if !ok || baseline == nil || from.Dialect() != "postgresql" {
		return diff.Plan{}, fmt.Errorf("postgresql schema diff requires a PostgreSQL baseline snapshot")
	}
	target, ok := to.(*schemaSnapshot)
	if !ok || target == nil || to.Dialect() != "postgresql" {
		return diff.Plan{}, fmt.Errorf("postgresql schema diff requires a PostgreSQL target snapshot")
	}

	comparison := diff.CompareSchemas(
		diff.Schema[tableDefinition, indexDefinition]{Tables: baseline.tables, Indexes: baseline.indexes},
		diff.Schema[tableDefinition, indexDefinition]{Tables: target.tables, Indexes: target.indexes},
		func(left, right tableDefinition) bool { return ast.Equal(left.statement, right.statement) },
		func(left, right indexDefinition) bool { return sameIndex(left.statement, right.statement) },
	)
	generated := make([]generatedStatement, 0)
	diagnostics := make([]string, 0)
	for _, entry := range comparison.Tables.Added {
		statement, err := createTableStatement(entry.Value.statement)
		if err != nil {
			return diff.Plan{}, err
		}
		generated = append(generated, statement)
	}
	for _, pair := range comparison.Tables.Matched {
		statements, tableDiagnostics, err := diffTable(pair.Baseline.statement, pair.Target.statement)
		if err != nil {
			return diff.Plan{}, err
		}
		generated = append(generated, statements...)
		diagnostics = append(diagnostics, tableDiagnostics...)
	}
	for _, entry := range comparison.Tables.Removed {
		diagnostics = append(diagnostics, fmt.Sprintf("table %s was removed", displayName(entry.Value.statement.Name)))
	}

	for _, entry := range comparison.Indexes.Added {
		if entry.Value.statement.Concurrently {
			diagnostics = append(diagnostics, fmt.Sprintf("index %s uses CONCURRENTLY, which needs non-transactional migration support", displayName(*entry.Value.statement.Name)))
			continue
		}
		statement, err := createIndexStatement(entry.Value.statement)
		if err != nil {
			return diff.Plan{}, err
		}
		generated = append(generated, statement)
	}
	for _, pair := range comparison.Indexes.Matched {
		if !pair.Equal {
			diagnostics = append(diagnostics, fmt.Sprintf("index %s changed", displayName(*pair.Target.statement.Name)))
		}
	}
	for _, entry := range comparison.Indexes.Removed {
		diagnostics = append(diagnostics, fmt.Sprintf("index %s was removed", displayName(*entry.Value.statement.Name)))
	}
	if len(diagnostics) > 0 {
		return diff.Plan{}, manualMigrationError(diagnostics)
	}

	plan := diff.Plan{Dialect: "postgresql", Statements: make([]diff.Statement, len(generated))}
	for index, statement := range generated {
		plan.Statements[index] = diff.Statement{
			Source:  fmt.Sprintf("%03d_%s.sql", index+1, statement.name),
			SQL:     statement.sql,
			Summary: statement.summary,
		}
	}
	return plan, nil
}

type schemaSnapshot struct {
	tables  map[string]tableDefinition
	indexes map[string]indexDefinition
}

// Dialect identifies PostgreSQL snapshots.
func (*schemaSnapshot) Dialect() string {
	return "postgresql"
}

type tableDefinition struct {
	source    string
	statement *pgquery.CreateTableStatement
}

func (s *schemaSnapshot) addTable(source string, statement *pgquery.CreateTableStatement) error {
	key := qualifiedNameKey(statement.Name)
	if previous, exists := s.tables[key]; exists {
		return fmt.Errorf("postgresql schema source %q defines table %s already defined by %q", source, displayName(statement.Name), previous.source)
	}
	columns := make(map[string]struct{}, len(statement.Columns))
	for _, column := range statement.Columns {
		if _, exists := columns[column.Name.Name]; exists {
			return fmt.Errorf("postgresql schema source %q defines duplicate column %q in table %s", source, column.Name.Name, displayName(statement.Name))
		}
		columns[column.Name.Name] = struct{}{}
	}
	s.tables[key] = tableDefinition{source: source, statement: statement}
	return nil
}

type indexDefinition struct {
	source    string
	statement *pgquery.CreateIndexStatement
}

func (s *schemaSnapshot) addIndex(source string, statement *pgquery.CreateIndexStatement) error {
	if statement.Name == nil {
		return fmt.Errorf("postgresql schema source %q contains an unnamed index on %s", source, displayName(statement.Table))
	}
	key := qualifiedNameKey(*statement.Name)
	if previous, exists := s.indexes[key]; exists {
		return fmt.Errorf("postgresql schema source %q defines index %s already defined by %q", source, displayName(*statement.Name), previous.source)
	}
	s.indexes[key] = indexDefinition{source: source, statement: statement}
	return nil
}

type generatedStatement struct {
	name    string
	sql     string
	summary string
}

func createTableStatement(table *pgquery.CreateTableStatement) (generatedStatement, error) {
	copy := *table
	copy.IfNotExists = false
	sql, err := serialize(&copy)
	if err != nil {
		return generatedStatement{}, err
	}
	name := displayName(copy.Name)
	return generatedStatement{
		name:    "create_table_" + filenamePart(name),
		sql:     sql,
		summary: "create table " + name,
	}, nil
}

func createIndexStatement(index *pgquery.CreateIndexStatement) (generatedStatement, error) {
	copy := *index
	copy.IfNotExists = false
	sql, err := serialize(&copy)
	if err != nil {
		return generatedStatement{}, err
	}
	name := displayName(*copy.Name)
	return generatedStatement{
		name:    "create_index_" + filenamePart(name),
		sql:     sql,
		summary: "create index " + name,
	}, nil
}

func diffTable(baseline *pgquery.CreateTableStatement, target *pgquery.CreateTableStatement) ([]generatedStatement, []string, error) {
	generated := make([]generatedStatement, 0)
	diagnostics := make([]string, 0)
	if baseline.Persistence != target.Persistence {
		diagnostics = append(diagnostics, fmt.Sprintf("table %s persistence changed", displayName(target.Name)))
	}
	if !ast.Equal(baseline.Constraints, target.Constraints) {
		diagnostics = append(diagnostics, fmt.Sprintf("table %s constraints changed", displayName(target.Name)))
	}

	baselineColumns := make(map[string]pgquery.ColumnDefinition, len(baseline.Columns))
	for _, column := range baseline.Columns {
		baselineColumns[column.Name.Name] = column
	}
	targetColumns := make(map[string]pgquery.ColumnDefinition, len(target.Columns))
	for _, column := range target.Columns {
		targetColumns[column.Name.Name] = column
	}
	for _, column := range target.Columns {
		previous, exists := baselineColumns[column.Name.Name]
		if !exists {
			if columnRequiresBackfill(column) {
				diagnostics = append(diagnostics, fmt.Sprintf("new required column %s.%s needs an application-specific backfill", displayName(target.Name), column.Name.Name))
				continue
			}
			statement := &pgquery.AlterTableStatement{
				Name: target.Name,
				Actions: []pgquery.AlterTableAction{{
					Kind:   pgquery.AlterTableAddColumn,
					Column: &column,
				}},
			}
			sql, err := serialize(statement)
			if err != nil {
				return nil, nil, err
			}
			name := displayName(target.Name)
			generated = append(generated, generatedStatement{
				name:    "add_column_" + filenamePart(name) + "_" + filenamePart(column.Name.Name),
				sql:     sql,
				summary: "add column " + name + "." + column.Name.Name,
			})
			continue
		}
		if !ast.Equal(previous, column) {
			diagnostics = append(diagnostics, fmt.Sprintf("column %s.%s changed", displayName(target.Name), column.Name.Name))
		}
	}
	for _, column := range baseline.Columns {
		if _, exists := targetColumns[column.Name.Name]; !exists {
			diagnostics = append(diagnostics, fmt.Sprintf("column %s.%s was removed", displayName(baseline.Name), column.Name.Name))
		}
	}
	return generated, diagnostics, nil
}

func columnRequiresBackfill(column pgquery.ColumnDefinition) bool {
	hasDefault := false
	for _, constraint := range column.Constraints {
		if constraint.Kind == pgquery.ConstraintDefault {
			hasDefault = true
		}
	}
	for _, constraint := range column.Constraints {
		if constraint.Kind == pgquery.ConstraintPrimaryKey {
			return true
		}
		if constraint.Kind == pgquery.ConstraintNotNull {
			return !hasDefault
		}
	}
	return false
}

func sameIndex(left *pgquery.CreateIndexStatement, right *pgquery.CreateIndexStatement) bool {
	leftCopy := *left
	leftCopy.IfNotExists = false
	rightCopy := *right
	rightCopy.IfNotExists = false
	return ast.Equal(leftCopy, rightCopy)
}

func serialize(statement pgquery.Statement) (string, error) {
	sql, err := pgquery.SerializeStatement(statement)
	if err != nil {
		return "", fmt.Errorf("postgresql schema diff: serialize generated statement: %w", err)
	}
	return sql + ";\n", nil
}

func manualMigrationError(diagnostics []string) error {
	sort.Strings(diagnostics)
	lines := make([]string, len(diagnostics))
	for index, diagnostic := range diagnostics {
		lines[index] = "- " + diagnostic
	}
	return fmt.Errorf("postgresql schema diff requires manual migration:\n%s", strings.Join(lines, "\n"))
}

func sortedTableKeys(tables map[string]tableDefinition) []string {
	keys := make([]string, 0, len(tables))
	for key := range tables {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedIndexKeys(indexes map[string]indexDefinition) []string {
	keys := make([]string, 0, len(indexes))
	for key := range indexes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func qualifiedNameKey(name pgquery.QualifiedName) string {
	var key strings.Builder
	for _, part := range name {
		fmt.Fprintf(&key, "%d:%s", len(part.Name), part.Name)
	}
	return key.String()
}

func displayName(name pgquery.QualifiedName) string {
	return name.String()
}

func filenamePart(value string) string {
	var result strings.Builder
	previousUnderscore := false
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			result.WriteRune(unicode.ToLower(character))
			previousUnderscore = false
			continue
		}
		if !previousUnderscore {
			result.WriteByte('_')
			previousUnderscore = true
		}
	}
	name := strings.Trim(result.String(), "_")
	if name == "" {
		return "object"
	}
	return name
}
