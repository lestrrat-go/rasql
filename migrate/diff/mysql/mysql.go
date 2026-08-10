// Package mysql compares MySQL desired-schema sources.
package mysql

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	mysqlquery "github.com/lestrrat-go/rasql-mysql/query"
	"github.com/lestrrat-go/rasql/migrate/diff"
)

// LowerCaseTableNames controls MySQL table-name matching.
//
// The values match MySQL's lower_case_table_names system variable: zero keeps
// table names case-sensitive, one lowercases stored names, and two preserves
// stored spelling while comparing names case-insensitively.
type LowerCaseTableNames uint8

const (
	LowerCaseTableNamesCaseSensitive LowerCaseTableNames = iota
	LowerCaseTableNamesLowercase
	LowerCaseTableNamesPreserve
)

// Analyzer compares the supported MySQL desired-schema subset.
type Analyzer struct {
	lowerCaseTableNames LowerCaseTableNames
}

// New creates a MySQL desired-schema analyzer.
func New() Analyzer {
	return Analyzer{}
}

// NewWithLowerCaseTableNames creates a MySQL analyzer using the supplied
// lower_case_table_names behavior for table matching.
func NewWithLowerCaseTableNames(value LowerCaseTableNames) Analyzer {
	return Analyzer{lowerCaseTableNames: value}
}

// Dialect identifies MySQL schema sources.
func (Analyzer) Dialect() string {
	return "mysql"
}

// Parse reads CREATE TABLE and named CREATE INDEX statements from sources.
func (a Analyzer) Parse(sources []diff.Source) (diff.Snapshot, error) {
	snapshot := &schemaSnapshot{
		tables:              make(map[string]tableDefinition),
		indexes:             make(map[indexKey]indexDefinition),
		lowerCaseTableNames: a.lowerCaseTableNames,
	}
	for _, source := range sources {
		parsed, err := mysqlquery.Parse(source.SQL)
		if err != nil {
			return nil, fmt.Errorf("mysql schema source %q: %w", source.Path, err)
		}
		for index, statement := range parsed.Statements {
			switch statement := statement.(type) {
			case *mysqlquery.CreateTableStatement:
				if err := snapshot.addTable(source.Path, statement); err != nil {
					return nil, err
				}
			case *mysqlquery.CreateIndexStatement:
				if err := snapshot.addIndex(source.Path, statement); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("mysql schema source %q statement %d must be CREATE TABLE or named CREATE INDEX, got %T", source.Path, index+1, statement)
			}
		}
	}
	if len(snapshot.tables) == 0 {
		return nil, fmt.Errorf("mysql schema has no CREATE TABLE statements")
	}
	return snapshot, nil
}

// Diff returns safe, additive changes from from to to.
func (a Analyzer) Diff(from diff.Snapshot, to diff.Snapshot) (diff.Plan, error) {
	baseline, ok := from.(*schemaSnapshot)
	if !ok || baseline == nil || from.Dialect() != "mysql" {
		return diff.Plan{}, fmt.Errorf("mysql schema diff requires a MySQL baseline snapshot")
	}
	target, ok := to.(*schemaSnapshot)
	if !ok || target == nil || to.Dialect() != "mysql" {
		return diff.Plan{}, fmt.Errorf("mysql schema diff requires a MySQL target snapshot")
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
		statements, tableDiagnostics, err := diffTable(baselineTable.statement, targetTable.statement, a.lowerCaseTableNames)
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
		if !sameIndex(baselineIndex.statement, targetIndex.statement, a.lowerCaseTableNames) {
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

	plan := diff.Plan{Dialect: "mysql", Statements: make([]diff.Statement, len(generated))}
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
	tables              map[string]tableDefinition
	indexes             map[indexKey]indexDefinition
	lowerCaseTableNames LowerCaseTableNames
}

// Dialect identifies MySQL snapshots.
func (*schemaSnapshot) Dialect() string {
	return "mysql"
}

type tableDefinition struct {
	source    string
	statement *mysqlquery.CreateTableStatement
}

func (s *schemaSnapshot) addTable(source string, statement *mysqlquery.CreateTableStatement) error {
	key := tableNameKey(statement.Name, s.lowerCaseTableNames)
	if previous, exists := s.tables[key]; exists {
		return fmt.Errorf("mysql schema source %q defines table %s already defined by %q", source, displayName(statement.Name), previous.source)
	}
	columns := make(map[string]struct{}, len(statement.Columns))
	for _, column := range statement.Columns {
		key := columnNameKey(column.Name.Name)
		if _, exists := columns[key]; exists {
			return fmt.Errorf("mysql schema source %q defines duplicate column %q in table %s", source, column.Name.Name, displayName(statement.Name))
		}
		columns[key] = struct{}{}
	}
	s.tables[key] = tableDefinition{source: source, statement: statement}
	return nil
}

type indexDefinition struct {
	source    string
	statement *mysqlquery.CreateIndexStatement
}

type indexKey struct {
	owner string
	name  string
}

func (s *schemaSnapshot) addIndex(source string, statement *mysqlquery.CreateIndexStatement) error {
	key := indexKey{
		owner: qualifiedNameKey(statement.Table, s.lowerCaseTableNames != LowerCaseTableNamesCaseSensitive),
		name:  qualifiedNameKey(statement.Name, true),
	}
	if previous, exists := s.indexes[key]; exists {
		return fmt.Errorf("mysql schema source %q defines index %s already defined by %q", source, displayName(statement.Name), previous.source)
	}
	s.indexes[key] = indexDefinition{source: source, statement: statement}
	return nil
}

type generatedStatement struct {
	name    string
	sql     string
	summary string
}

const (
	maxFilenamePartBytes = 100
	filenamePartHashSize = 12
)

func createTableStatement(table *mysqlquery.CreateTableStatement) (generatedStatement, error) {
	copy := *table
	copy.IfNotExists = false
	sql, err := serialize(&copy)
	if err != nil {
		return generatedStatement{}, err
	}
	name := displayName(copy.Name)
	// WriteMigration prefixes each generated statement with its sequence position, so equal normalized
	// components remain distinct in the public migration output; this is covered by its ordered plan.
	return generatedStatement{
		name:    "create_table_" + filenamePart(name),
		sql:     sql,
		summary: "create table " + name,
	}, nil
}

func createIndexStatement(index *mysqlquery.CreateIndexStatement) (generatedStatement, error) {
	copy := *index
	copy.IfNotExists = false
	sql, err := serialize(&copy)
	if err != nil {
		return generatedStatement{}, err
	}
	name := displayName(copy.Name)
	return generatedStatement{
		name:    "create_index_" + filenamePart(displayName(copy.Table)) + "_" + filenamePart(name),
		sql:     sql,
		summary: "create index " + name,
	}, nil
}

func diffTable(baseline *mysqlquery.CreateTableStatement, target *mysqlquery.CreateTableStatement, tableNames LowerCaseTableNames) ([]generatedStatement, []string, error) {
	generated := make([]generatedStatement, 0)
	diagnostics := make([]string, 0)
	if baseline.Persistence != target.Persistence {
		diagnostics = append(diagnostics, fmt.Sprintf("table %s persistence changed", displayName(target.Name)))
	}
	if !sameTableConstraints(baseline.Constraints, target.Constraints, tableNames) {
		diagnostics = append(diagnostics, fmt.Sprintf("table %s constraints changed", displayName(target.Name)))
	}

	baselineColumns := make(map[string]mysqlquery.ColumnDefinition, len(baseline.Columns))
	for _, column := range baseline.Columns {
		baselineColumns[columnNameKey(column.Name.Name)] = column
	}
	targetColumns := make(map[string]mysqlquery.ColumnDefinition, len(target.Columns))
	for _, column := range target.Columns {
		targetColumns[columnNameKey(column.Name.Name)] = column
	}
	for _, column := range target.Columns {
		previous, exists := baselineColumns[columnNameKey(column.Name.Name)]
		if !exists {
			if columnRequiresBackfill(column) {
				diagnostics = append(diagnostics, fmt.Sprintf("new required column %s.%s needs an application-specific backfill", displayName(target.Name), column.Name.Name))
				continue
			}
			statement := &mysqlquery.AlterTableStatement{
				Name: target.Name,
				Actions: []mysqlquery.AlterTableAction{{
					Kind:   mysqlquery.AlterTableAddColumn,
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
		if !sameColumn(previous, column, tableNames) {
			diagnostics = append(diagnostics, fmt.Sprintf("column %s.%s changed", displayName(target.Name), column.Name.Name))
		}
	}
	for _, column := range baseline.Columns {
		if _, exists := targetColumns[columnNameKey(column.Name.Name)]; !exists {
			diagnostics = append(diagnostics, fmt.Sprintf("column %s.%s was removed", displayName(baseline.Name), column.Name.Name))
		}
	}
	return generated, diagnostics, nil
}

func sameColumn(left mysqlquery.ColumnDefinition, right mysqlquery.ColumnDefinition, tableNames LowerCaseTableNames) bool {
	left = normalizedColumn(left, tableNames)
	right = normalizedColumn(right, tableNames)
	return reflect.DeepEqual(left, right)
}

func sameTableConstraints(left []mysqlquery.TableConstraint, right []mysqlquery.TableConstraint, tableNames LowerCaseTableNames) bool {
	left = normalizedTableConstraints(left, tableNames)
	right = normalizedTableConstraints(right, tableNames)
	return reflect.DeepEqual(left, right)
}

func normalizedColumn(column mysqlquery.ColumnDefinition, tableNames LowerCaseTableNames) mysqlquery.ColumnDefinition {
	column.Name = normalizedIdentifier(column.Name)
	if column.Constraints == nil {
		return column
	}
	constraints := make([]mysqlquery.ColumnConstraint, len(column.Constraints))
	for index, constraint := range column.Constraints {
		constraints[index] = normalizedColumnConstraint(constraint, tableNames)
	}
	column.Constraints = constraints
	return column
}

func normalizedColumnConstraint(constraint mysqlquery.ColumnConstraint, tableNames LowerCaseTableNames) mysqlquery.ColumnConstraint {
	if constraint.Name != nil {
		name := normalizedIdentifier(*constraint.Name)
		constraint.Name = &name
	}
	constraint.Expression = normalizedExpression(constraint.Expression)
	constraint.References = normalizedReference(constraint.References, tableNames)
	return constraint
}

func normalizedTableConstraints(constraints []mysqlquery.TableConstraint, tableNames LowerCaseTableNames) []mysqlquery.TableConstraint {
	if constraints == nil {
		return nil
	}
	normalized := make([]mysqlquery.TableConstraint, len(constraints))
	for index, constraint := range constraints {
		normalized[index] = constraint
		if constraint.Name != nil {
			name := normalizedIdentifier(*constraint.Name)
			normalized[index].Name = &name
		}
		if constraint.Columns != nil {
			normalized[index].Columns = normalizedIdentifiers(constraint.Columns)
		}
		normalized[index].Expression = normalizedExpression(constraint.Expression)
		normalized[index].References = normalizedReference(constraint.References, tableNames)
	}
	return normalized
}

func normalizedReference(reference *mysqlquery.Reference, tableNames LowerCaseTableNames) *mysqlquery.Reference {
	if reference == nil {
		return nil
	}
	normalized := *reference
	normalized.Table = normalizedQualifiedName(reference.Table, tableNames != LowerCaseTableNamesCaseSensitive)
	if reference.Columns != nil {
		normalized.Columns = normalizedIdentifiers(reference.Columns)
	}
	return &normalized
}

func normalizedIndex(index *mysqlquery.CreateIndexStatement, tableNames LowerCaseTableNames) mysqlquery.CreateIndexStatement {
	normalized := *index
	normalized.IfNotExists = false
	normalized.Name = normalizedQualifiedName(index.Name, true)
	normalized.Table = normalizedQualifiedName(index.Table, tableNames != LowerCaseTableNamesCaseSensitive)
	if index.Method != nil {
		method := normalizedIdentifier(*index.Method)
		normalized.Method = &method
	}
	if index.Elements != nil {
		normalized.Elements = make([]mysqlquery.IndexElement, len(index.Elements))
		for elementIndex, element := range index.Elements {
			normalized.Elements[elementIndex] = element
			normalized.Elements[elementIndex].Expression = normalizedExpression(element.Expression)
		}
	}
	return normalized
}

func normalizedExpression(expression mysqlquery.Expression) mysqlquery.Expression {
	switch expression := expression.(type) {
	case *mysqlquery.IdentifierExpression:
		if expression == nil {
			return nil
		}
		normalized := *expression
		normalized.Name = normalizedQualifiedName(expression.Name, true)
		return &normalized
	case *mysqlquery.StarExpression:
		if expression == nil {
			return nil
		}
		normalized := *expression
		normalized.Qualifier = normalizedQualifiedName(expression.Qualifier, true)
		return &normalized
	case *mysqlquery.UnaryExpression:
		if expression == nil {
			return nil
		}
		normalized := *expression
		normalized.Expression = normalizedExpression(expression.Expression)
		return &normalized
	case *mysqlquery.BinaryExpression:
		if expression == nil {
			return nil
		}
		normalized := *expression
		normalized.Left = normalizedExpression(expression.Left)
		normalized.Right = normalizedExpression(expression.Right)
		return &normalized
	case *mysqlquery.CallExpression:
		if expression == nil {
			return nil
		}
		normalized := *expression
		normalized.Function = normalizedQualifiedName(expression.Function, true)
		if expression.Arguments != nil {
			normalized.Arguments = make([]mysqlquery.Expression, len(expression.Arguments))
			for index, argument := range expression.Arguments {
				normalized.Arguments[index] = normalizedExpression(argument)
			}
		}
		return &normalized
	default:
		return expression
	}
}

func normalizedIdentifiers(identifiers []mysqlquery.Identifier) []mysqlquery.Identifier {
	normalized := make([]mysqlquery.Identifier, len(identifiers))
	for index, identifier := range identifiers {
		normalized[index] = normalizedIdentifier(identifier)
	}
	return normalized
}

func normalizedIdentifier(identifier mysqlquery.Identifier) mysqlquery.Identifier {
	identifier.Name = strings.ToLower(identifier.Name)
	return identifier
}

func normalizedQualifiedName(name mysqlquery.QualifiedName, caseInsensitive bool) mysqlquery.QualifiedName {
	if name == nil {
		return nil
	}
	normalized := make(mysqlquery.QualifiedName, len(name))
	for index, identifier := range name {
		if caseInsensitive {
			normalized[index] = normalizedIdentifier(identifier)
			continue
		}
		normalized[index] = identifier
	}
	return normalized
}

func columnRequiresBackfill(column mysqlquery.ColumnDefinition) bool {
	hasDefault := false
	defaultIsNull := false
	hasNotNull := false
	hasPrimaryKey := false
	for _, constraint := range column.Constraints {
		switch constraint.Kind {
		case mysqlquery.ConstraintDefault:
			hasDefault = true
			literal, ok := constraint.Expression.(*mysqlquery.Literal)
			defaultIsNull = ok && literal.Kind == mysqlquery.NullLiteral
		case mysqlquery.ConstraintNotNull:
			hasNotNull = true
		case mysqlquery.ConstraintPrimaryKey:
			hasPrimaryKey = true
		}
	}
	if hasPrimaryKey {
		return true
	}
	return hasNotNull && (!hasDefault || defaultIsNull)
}

func sameIndex(left *mysqlquery.CreateIndexStatement, right *mysqlquery.CreateIndexStatement, tableNames LowerCaseTableNames) bool {
	leftCopy := normalizedIndex(left, tableNames)
	rightCopy := normalizedIndex(right, tableNames)
	leftCopy.IfNotExists = false
	rightCopy.IfNotExists = false
	return reflect.DeepEqual(leftCopy, rightCopy)
}

func serialize(statement mysqlquery.Statement) (string, error) {
	sql, err := mysqlquery.SerializeStatement(statement)
	if err != nil {
		return "", fmt.Errorf("mysql schema diff: serialize generated statement: %w", err)
	}
	return sql + ";\n", nil
}

func manualMigrationError(diagnostics []string) error {
	sort.Strings(diagnostics)
	lines := make([]string, len(diagnostics))
	for index, diagnostic := range diagnostics {
		lines[index] = "- " + diagnostic
	}
	return fmt.Errorf("mysql schema diff requires manual migration:\n%s", strings.Join(lines, "\n"))
}

func sortedTableKeys(tables map[string]tableDefinition) []string {
	keys := make([]string, 0, len(tables))
	for key := range tables {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedIndexKeys(indexes map[indexKey]indexDefinition) []indexKey {
	keys := make([]indexKey, 0, len(indexes))
	for key := range indexes {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		if keys[left].owner != keys[right].owner {
			return keys[left].owner < keys[right].owner
		}
		return keys[left].name < keys[right].name
	})
	return keys
}

func tableNameKey(name mysqlquery.QualifiedName, tableNames LowerCaseTableNames) string {
	return qualifiedNameKey(name, tableNames != LowerCaseTableNamesCaseSensitive)
}

func columnNameKey(name string) string {
	return strings.ToLower(name)
}

func qualifiedNameKey(name mysqlquery.QualifiedName, caseInsensitive bool) string {
	var key strings.Builder
	for _, part := range name {
		value := part.Name
		if caseInsensitive {
			value = strings.ToLower(value)
		}
		fmt.Fprintf(&key, "%d:%s", len(value), value)
	}
	return key.String()
}

func displayName(name mysqlquery.QualifiedName) string {
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
	if len(name) <= maxFilenamePartBytes {
		return name
	}
	hash := sha256.Sum256([]byte(value))
	suffix := fmt.Sprintf("_%x", hash[:filenamePartHashSize])
	return truncateFilenamePart(name, maxFilenamePartBytes-len(suffix)) + suffix
}

func truncateFilenamePart(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
