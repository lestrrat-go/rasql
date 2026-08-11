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
func (Analyzer) LiveSources(table schema.TableDef) ([]diff.Source, error) {
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
	for _, key := range sortedIndexKeys(snapshot.indexes) {
		index := snapshot.indexes[key]
		if _, exists := snapshot.tables[qualifiedNameKey(index.statement.Table)]; !exists {
			return nil, fmt.Errorf("postgresql schema source %q defines index %s on missing table %s", index.source, displayName(*index.statement.Name), displayName(index.statement.Table))
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
		statements, tableDiagnostics, err := diffTable(pair.Baseline, pair.Target)
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
		if !sameIndex(pair.Baseline.statement, pair.Target.statement) {
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

func sortedIndexKeys(indexes map[string]indexDefinition) []string {
	keys := make([]string, 0, len(indexes))
	for key := range indexes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
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

func diffTable(baseline, target tableDefinition) ([]generatedStatement, []string, error) {
	generated := make([]generatedStatement, 0)
	diagnostics := make([]string, 0)
	normalizedBaseline := normalizedTable(baseline.statement, false)
	normalizedTarget := normalizedTable(target.statement, false)
	if baseline.statement.Persistence != target.statement.Persistence {
		diagnostics = append(diagnostics, fmt.Sprintf("table %s persistence changed", displayName(target.statement.Name)))
	}
	if !ast.Equal(normalizedBaseline.Constraints, normalizedTarget.Constraints) {
		diagnostics = append(diagnostics, fmt.Sprintf("table %s constraints changed", displayName(target.statement.Name)))
	}

	baselineColumns := make(map[string]pgquery.ColumnDefinition, len(baseline.statement.Columns))
	for _, column := range normalizedBaseline.Columns {
		baselineColumns[column.Name.Name] = column
	}
	targetColumns := make(map[string]pgquery.ColumnDefinition, len(target.statement.Columns))
	for _, column := range normalizedTarget.Columns {
		targetColumns[column.Name.Name] = column
	}
	for index, column := range target.statement.Columns {
		normalizedColumn := normalizedTarget.Columns[index]
		previous, exists := baselineColumns[normalizedColumn.Name.Name]
		if !exists {
			if columnRequiresBackfill(column) {
				diagnostics = append(diagnostics, fmt.Sprintf("new required column %s.%s needs an application-specific backfill", displayName(target.statement.Name), column.Name.Name))
				continue
			}
			statement := &pgquery.AlterTableStatement{
				Name: target.statement.Name,
				Actions: []pgquery.AlterTableAction{{
					Kind:   pgquery.AlterTableAddColumn,
					Column: &column,
				}},
			}
			sql, err := serialize(statement)
			if err != nil {
				return nil, nil, err
			}
			name := displayName(target.statement.Name)
			generated = append(generated, generatedStatement{
				name:    "add_column_" + filenamePart(name) + "_" + filenamePart(column.Name.Name),
				sql:     sql,
				summary: "add column " + name + "." + column.Name.Name,
			})
			continue
		}
		if !ast.Equal(previous, normalizedColumn) {
			diagnostics = append(diagnostics, fmt.Sprintf("column %s.%s changed", displayName(target.statement.Name), column.Name.Name))
		}
	}
	for index, column := range baseline.statement.Columns {
		normalizedColumn := normalizedBaseline.Columns[index]
		if _, exists := targetColumns[normalizedColumn.Name.Name]; !exists {
			diagnostics = append(diagnostics, fmt.Sprintf("column %s.%s was removed", displayName(baseline.statement.Name), column.Name.Name))
		}
	}
	return generated, diagnostics, nil
}

// normalizedTable converts syntax variants that describe the same PostgreSQL
// table into one comparison form. PostgreSQL primary keys imply NOT NULL, and
// the live renderer writes them as table constraints with quoted identifiers.
func normalizedTable(table *pgquery.CreateTableStatement, ignoreQuotes bool) pgquery.CreateTableStatement {
	normalized := *table
	normalized.Constraints = normalizedTableConstraints(table.Constraints, ignoreQuotes)
	normalized.Columns = make([]pgquery.ColumnDefinition, len(table.Columns))
	inlinePrimaryKeys := make([]pgquery.TableConstraint, 0)
	for index, column := range table.Columns {
		normalizedColumn := normalizedColumn(column, ignoreQuotes)
		constraints := make([]pgquery.ColumnConstraint, 0, len(normalizedColumn.Constraints))
		for _, constraint := range normalizedColumn.Constraints {
			if constraint.Kind == pgquery.ConstraintPrimaryKey {
				inlinePrimaryKeys = append(inlinePrimaryKeys, pgquery.TableConstraint{
					Name: constraint.Name,
					Kind: pgquery.ConstraintPrimaryKey,
					Columns: []pgquery.Identifier{{
						Name:   normalizedColumn.Name.Name,
						Quoted: normalizedColumn.Name.Quoted,
					}},
				})
				continue
			}
			constraints = append(constraints, constraint)
		}
		normalizedColumn.Constraints = constraints
		normalized.Columns[index] = normalizedColumn
	}
	normalized.Constraints = append(inlinePrimaryKeys, normalized.Constraints...)
	primaryKeyColumns := make(map[string]struct{})
	for _, constraint := range normalized.Constraints {
		if constraint.Kind != pgquery.ConstraintPrimaryKey {
			continue
		}
		for _, column := range constraint.Columns {
			primaryKeyColumns[column.Name] = struct{}{}
		}
	}
	for index := range normalized.Columns {
		column := &normalized.Columns[index]
		if _, ok := primaryKeyColumns[column.Name.Name]; !ok || hasColumnConstraint(column.Constraints, pgquery.ConstraintNotNull) {
			continue
		}
		column.Constraints = append(column.Constraints, pgquery.ColumnConstraint{Kind: pgquery.ConstraintNotNull})
	}
	return normalized
}

func normalizedColumn(column pgquery.ColumnDefinition, ignoreQuotes bool) pgquery.ColumnDefinition {
	normalized := column
	normalized.Name = normalizedIdentifier(column.Name, ignoreQuotes)
	if column.Constraints == nil {
		return normalized
	}
	normalized.Constraints = make([]pgquery.ColumnConstraint, len(column.Constraints))
	for index, constraint := range column.Constraints {
		normalized.Constraints[index] = normalizedColumnConstraint(constraint, ignoreQuotes)
	}
	return normalized
}

func normalizedColumnConstraint(constraint pgquery.ColumnConstraint, ignoreQuotes bool) pgquery.ColumnConstraint {
	normalized := constraint
	normalized.Name = normalizedIdentifierPtr(constraint.Name, ignoreQuotes)
	normalized.Expression = normalizedExpression(constraint.Expression, ignoreQuotes)
	normalized.References = normalizedReference(constraint.References, ignoreQuotes)
	return normalized
}

func normalizedTableConstraints(constraints []pgquery.TableConstraint, ignoreQuotes bool) []pgquery.TableConstraint {
	if constraints == nil {
		return nil
	}
	normalized := make([]pgquery.TableConstraint, len(constraints))
	for index, constraint := range constraints {
		normalized[index] = constraint
		normalized[index].Name = normalizedIdentifierPtr(constraint.Name, ignoreQuotes)
		normalized[index].Columns = normalizedIdentifiers(constraint.Columns, ignoreQuotes)
		normalized[index].Expression = normalizedExpression(constraint.Expression, ignoreQuotes)
		normalized[index].References = normalizedReference(constraint.References, ignoreQuotes)
	}
	return normalized
}

func normalizedReference(reference *pgquery.Reference, ignoreQuotes bool) *pgquery.Reference {
	if reference == nil {
		return nil
	}
	normalized := *reference
	normalized.Table = normalizedQualifiedName(reference.Table, ignoreQuotes)
	normalized.Columns = normalizedIdentifiers(reference.Columns, ignoreQuotes)
	return &normalized
}

func normalizedExpression(expression pgquery.Expression, ignoreQuotes bool) pgquery.Expression {
	switch expression := expression.(type) {
	case *pgquery.IdentifierExpression:
		if expression == nil {
			return nil
		}
		normalized := *expression
		normalized.Name = normalizedQualifiedName(expression.Name, ignoreQuotes)
		return &normalized
	case *pgquery.StarExpression:
		if expression == nil {
			return nil
		}
		normalized := *expression
		normalized.Qualifier = normalizedQualifiedName(expression.Qualifier, ignoreQuotes)
		return &normalized
	case *pgquery.UnaryExpression:
		if expression == nil {
			return nil
		}
		normalized := *expression
		normalized.Expression = normalizedExpression(expression.Expression, ignoreQuotes)
		return &normalized
	case *pgquery.BinaryExpression:
		if expression == nil {
			return nil
		}
		normalized := *expression
		normalized.Left = normalizedExpression(expression.Left, ignoreQuotes)
		normalized.Right = normalizedExpression(expression.Right, ignoreQuotes)
		return &normalized
	case *pgquery.CallExpression:
		if expression == nil {
			return nil
		}
		normalized := *expression
		normalized.Function = normalizedQualifiedName(expression.Function, ignoreQuotes)
		if expression.Arguments != nil {
			normalized.Arguments = make([]pgquery.Expression, len(expression.Arguments))
			for index, argument := range expression.Arguments {
				normalized.Arguments[index] = normalizedExpression(argument, ignoreQuotes)
			}
		}
		return &normalized
	default:
		return expression
	}
}

func normalizedIdentifiers(identifiers []pgquery.Identifier, ignoreQuotes bool) []pgquery.Identifier {
	if identifiers == nil {
		return nil
	}
	normalized := make([]pgquery.Identifier, len(identifiers))
	for index, identifier := range identifiers {
		normalized[index] = normalizedIdentifier(identifier, ignoreQuotes)
	}
	return normalized
}

func normalizedIdentifierPtr(identifier *pgquery.Identifier, ignoreQuotes bool) *pgquery.Identifier {
	if identifier == nil {
		return nil
	}
	normalized := normalizedIdentifier(*identifier, ignoreQuotes)
	return &normalized
}

func normalizedIdentifier(identifier pgquery.Identifier, ignoreQuotes bool) pgquery.Identifier {
	if ignoreQuotes {
		identifier.Quoted = false
	}
	return identifier
}

func normalizedQualifiedName(name pgquery.QualifiedName, ignoreQuotes bool) pgquery.QualifiedName {
	if name == nil {
		return nil
	}
	normalized := make(pgquery.QualifiedName, len(name))
	for index, identifier := range name {
		normalized[index] = normalizedIdentifier(identifier, ignoreQuotes)
	}
	return normalized
}

func hasColumnConstraint(constraints []pgquery.ColumnConstraint, kind pgquery.ConstraintKind) bool {
	for _, constraint := range constraints {
		if constraint.Kind == kind {
			return true
		}
	}
	return false
}

func columnRequiresBackfill(column pgquery.ColumnDefinition) bool {
	hasDefault := false
	defaultIsNull := false
	hasNotNull := false
	hasPrimaryKey := false
	for _, constraint := range column.Constraints {
		switch constraint.Kind {
		case pgquery.ConstraintDefault:
			hasDefault = true
			literal, ok := constraint.Expression.(*pgquery.Literal)
			defaultIsNull = ok && literal.Kind == pgquery.NullLiteral
		case pgquery.ConstraintNotNull:
			hasNotNull = true
		case pgquery.ConstraintPrimaryKey:
			hasPrimaryKey = true
		}
	}
	if hasPrimaryKey {
		return true
	}
	return hasNotNull && (!hasDefault || defaultIsNull)
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
