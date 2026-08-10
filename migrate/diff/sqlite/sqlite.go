// Package sqlite compares SQLite desired-schema sources.
package sqlite

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode"

	sqlitequery "github.com/lestrrat-go/rasql-sqlite/query"
	"github.com/lestrrat-go/rasql/migrate/diff"
)

// Analyzer compares the supported SQLite desired-schema subset.
type Analyzer struct{}

// New creates a SQLite desired-schema analyzer.
func New() Analyzer {
	return Analyzer{}
}

// Dialect identifies SQLite schema sources.
func (Analyzer) Dialect() string {
	return "sqlite"
}

// Parse reads CREATE TABLE and named CREATE INDEX statements from sources.
func (Analyzer) Parse(sources []diff.Source) (diff.Snapshot, error) {
	snapshot := &schemaSnapshot{
		tables:  make(map[string]tableDefinition),
		indexes: make(map[string]indexDefinition),
	}
	for _, source := range sources {
		parsed, err := sqlitequery.Parse(source.SQL)
		if err != nil {
			return nil, fmt.Errorf("sqlite schema source %q: %w", source.Path, err)
		}
		for index, statement := range parsed.Statements {
			switch statement := statement.(type) {
			case *sqlitequery.CreateTableStatement:
				if err := snapshot.addTable(source.Path, statement); err != nil {
					return nil, err
				}
			case *sqlitequery.CreateIndexStatement:
				if err := snapshot.addIndex(source.Path, statement); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("sqlite schema source %q statement %d must be CREATE TABLE or named CREATE INDEX, got %T", source.Path, index+1, statement)
			}
		}
	}
	if len(snapshot.tables) == 0 {
		return nil, fmt.Errorf("sqlite schema has no CREATE TABLE statements")
	}
	for _, key := range sortedIndexKeys(snapshot.indexes) {
		index := snapshot.indexes[key]
		if _, exists := snapshot.tables[qualifiedNameKey(index.statement.Table)]; !exists {
			return nil, fmt.Errorf("sqlite schema source %q defines index %s on missing table %s", index.source, displayName(index.statement.Name), displayName(index.statement.Table))
		}
	}
	return snapshot, nil
}

// Diff returns safe, additive changes from from to to.
func (Analyzer) Diff(from diff.Snapshot, to diff.Snapshot) (diff.Plan, error) {
	baseline, ok := from.(*schemaSnapshot)
	if !ok || baseline == nil || from.Dialect() != "sqlite" {
		return diff.Plan{}, fmt.Errorf("sqlite schema diff requires a SQLite baseline snapshot")
	}
	target, ok := to.(*schemaSnapshot)
	if !ok || target == nil || to.Dialect() != "sqlite" {
		return diff.Plan{}, fmt.Errorf("sqlite schema diff requires a SQLite target snapshot")
	}

	generated := make([]generatedStatement, 0)
	diagnostics := make([]string, 0)
	for _, key := range sortedTableKeys(target.tables) {
		targetTable := target.tables[key]
		baselineTable, exists := baseline.tables[key]
		if !exists {
			statement, err := createTableStatement(targetTable.statement)
			if err != nil {
				return diff.Plan{}, err
			}
			generated = append(generated, statement)
			continue
		}
		statements, tableDiagnostics, err := diffTable(baselineTable.statement, targetTable.statement)
		if err != nil {
			return diff.Plan{}, err
		}
		generated = append(generated, statements...)
		diagnostics = append(diagnostics, tableDiagnostics...)
	}
	for _, key := range sortedTableKeys(baseline.tables) {
		if _, exists := target.tables[key]; !exists {
			diagnostics = append(diagnostics, fmt.Sprintf("table %s was removed", displayName(baseline.tables[key].statement.Name)))
		}
	}

	for _, key := range sortedIndexKeys(target.indexes) {
		targetIndex := target.indexes[key]
		baselineIndex, exists := baseline.indexes[key]
		if !exists {
			statement, err := createIndexStatement(targetIndex.statement)
			if err != nil {
				return diff.Plan{}, err
			}
			generated = append(generated, statement)
			continue
		}
		if !sameIndex(baselineIndex.statement, targetIndex.statement) {
			diagnostics = append(diagnostics, fmt.Sprintf("index %s changed", displayName(targetIndex.statement.Name)))
		}
	}
	for _, key := range sortedIndexKeys(baseline.indexes) {
		if _, exists := target.indexes[key]; !exists {
			diagnostics = append(diagnostics, fmt.Sprintf("index %s was removed", displayName(baseline.indexes[key].statement.Name)))
		}
	}
	if len(diagnostics) > 0 {
		return diff.Plan{}, manualMigrationError(diagnostics)
	}

	plan := diff.Plan{Dialect: "sqlite", Statements: make([]diff.Statement, len(generated))}
	for index, statement := range generated {
		plan.Statements[index] = diff.Statement{
			Source:  statement.name + ".sql",
			SQL:     statement.sql,
			Summary: statement.summary,
		}
	}
	if len(plan.Statements) > 0 {
		if err := plan.Validate(); err != nil {
			return diff.Plan{}, err
		}
		for index := range plan.Statements {
			plan.Statements[index].Source = fmt.Sprintf("%03d_%s", index+1, plan.Statements[index].Source)
		}
	}
	return plan, nil
}

type schemaSnapshot struct {
	tables  map[string]tableDefinition
	indexes map[string]indexDefinition
}

// Dialect identifies SQLite snapshots.
func (*schemaSnapshot) Dialect() string {
	return "sqlite"
}

type tableDefinition struct {
	source    string
	statement *sqlitequery.CreateTableStatement
}

func (s *schemaSnapshot) addTable(source string, statement *sqlitequery.CreateTableStatement) error {
	if statement.As != nil {
		return fmt.Errorf("sqlite schema source %q contains CREATE TABLE AS SELECT for %s, which has no declared table shape", source, displayName(statement.Name))
	}
	key := qualifiedNameKey(statement.Name)
	if previous, exists := s.tables[key]; exists {
		return fmt.Errorf("sqlite schema source %q defines table %s already defined by %q", source, displayName(statement.Name), previous.source)
	}
	columns := make(map[string]struct{}, len(statement.Columns))
	for _, column := range statement.Columns {
		if _, exists := columns[column.Name.Name]; exists {
			return fmt.Errorf("sqlite schema source %q defines duplicate column %q in table %s", source, column.Name.Name, displayName(statement.Name))
		}
		columns[column.Name.Name] = struct{}{}
	}
	s.tables[key] = tableDefinition{source: source, statement: statement}
	return nil
}

type indexDefinition struct {
	source    string
	statement *sqlitequery.CreateIndexStatement
}

func (s *schemaSnapshot) addIndex(source string, statement *sqlitequery.CreateIndexStatement) error {
	key := qualifiedNameKey(statement.Name)
	if previous, exists := s.indexes[key]; exists {
		return fmt.Errorf("sqlite schema source %q defines index %s already defined by %q", source, displayName(statement.Name), previous.source)
	}
	s.indexes[key] = indexDefinition{source: source, statement: statement}
	return nil
}

type generatedStatement struct {
	name    string
	sql     string
	summary string
}

func createTableStatement(table *sqlitequery.CreateTableStatement) (generatedStatement, error) {
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

func createIndexStatement(index *sqlitequery.CreateIndexStatement) (generatedStatement, error) {
	copy := *index
	copy.IfNotExists = false
	sql, err := serialize(&copy)
	if err != nil {
		return generatedStatement{}, err
	}
	name := displayName(copy.Name)
	return generatedStatement{
		name:    "create_index_" + filenamePart(name),
		sql:     sql,
		summary: "create index " + name,
	}, nil
}

func diffTable(baseline *sqlitequery.CreateTableStatement, target *sqlitequery.CreateTableStatement) ([]generatedStatement, []string, error) {
	generated := make([]generatedStatement, 0)
	diagnostics := make([]string, 0)
	if baseline.Persistence != target.Persistence {
		diagnostics = append(diagnostics, fmt.Sprintf("table %s persistence changed", displayName(target.Name)))
	}
	if !reflect.DeepEqual(baseline.Options, target.Options) {
		diagnostics = append(diagnostics, fmt.Sprintf("table %s options changed", displayName(target.Name)))
	}
	if !reflect.DeepEqual(baseline.Constraints, target.Constraints) {
		diagnostics = append(diagnostics, fmt.Sprintf("table %s constraints changed", displayName(target.Name)))
	}

	baselineColumns := make(map[string]sqlitequery.ColumnDefinition, len(baseline.Columns))
	for _, column := range baseline.Columns {
		baselineColumns[column.Name.Name] = column
	}
	targetColumns := make(map[string]sqlitequery.ColumnDefinition, len(target.Columns))
	for _, column := range target.Columns {
		targetColumns[column.Name.Name] = column
	}
	for _, column := range target.Columns {
		previous, exists := baselineColumns[column.Name.Name]
		if !exists {
			if diagnostic := columnAddDiagnostic(column); diagnostic != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("new column %s.%s %s", displayName(target.Name), column.Name.Name, diagnostic))
				continue
			}
			statement := &sqlitequery.AlterTableStatement{
				Name: target.Name,
				Action: sqlitequery.AlterTableAction{
					Kind:   sqlitequery.AlterTableAddColumn,
					Column: &column,
				},
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
		if !reflect.DeepEqual(previous, column) {
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

func columnAddDiagnostic(column sqlitequery.ColumnDefinition) string {
	hasDefault := false
	defaultIsNull := false
	for _, constraint := range column.Constraints {
		if constraint.Kind != sqlitequery.ConstraintDefault {
			continue
		}
		hasDefault = true
		literal, ok := constraint.Expression.(*sqlitequery.Literal)
		if !ok || literal.Kind == sqlitequery.CurrentTimeLiteral {
			return "has a nonliteral default that SQLite ALTER TABLE cannot add"
		}
		defaultIsNull = literal.Kind == sqlitequery.NullLiteral
	}
	for _, constraint := range column.Constraints {
		switch constraint.Kind {
		case sqlitequery.ConstraintPrimaryKey:
			return "has a PRIMARY KEY constraint that SQLite ALTER TABLE cannot add"
		case sqlitequery.ConstraintUnique:
			return "has a UNIQUE constraint that SQLite ALTER TABLE cannot add"
		case sqlitequery.ConstraintGenerated:
			return "is generated and needs a manual SQLite migration"
		case sqlitequery.ConstraintReferences:
			if hasDefault && !defaultIsNull {
				return "has a foreign-key default that SQLite ALTER TABLE cannot add"
			}
		case sqlitequery.ConstraintNotNull:
			if !hasDefault || defaultIsNull {
				return "needs an application-specific backfill"
			}
		}
	}
	return ""
}

func sameIndex(left *sqlitequery.CreateIndexStatement, right *sqlitequery.CreateIndexStatement) bool {
	leftCopy := *left
	leftCopy.IfNotExists = false
	rightCopy := *right
	rightCopy.IfNotExists = false
	return reflect.DeepEqual(leftCopy, rightCopy)
}

func serialize(statement sqlitequery.Statement) (string, error) {
	sql, err := sqlitequery.SerializeStatement(statement)
	if err != nil {
		return "", fmt.Errorf("sqlite schema diff: serialize generated statement: %w", err)
	}
	return sql + ";\n", nil
}

func manualMigrationError(diagnostics []string) error {
	sort.Strings(diagnostics)
	lines := make([]string, len(diagnostics))
	for index, diagnostic := range diagnostics {
		lines[index] = "- " + diagnostic
	}
	return fmt.Errorf("sqlite schema diff requires manual migration:\n%s", strings.Join(lines, "\n"))
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

func qualifiedNameKey(name sqlitequery.QualifiedName) string {
	var key strings.Builder
	for _, part := range name {
		fmt.Fprintf(&key, "%d:%s", len(part.Name), part.Name)
	}
	return key.String()
}

func displayName(name sqlitequery.QualifiedName) string {
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
