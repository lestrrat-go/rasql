// Package sqlite compares SQLite desired-schema sources.
package sqlite

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	sqlitequery "github.com/lestrrat-go/rasql-sqlite/query"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/ast"
	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/schema"
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

// LiveSources converts one inspected SQLite table into desired-schema sources.
func (Analyzer) LiveSources(table schema.Table) ([]diff.Source, error) {
	return diff.SourcesFromTable(dialect.SQLite(), table)
}

// ValidateLivePlan ensures generated SQLite statements stay within the selected table.
func (Analyzer) ValidateLivePlan(plan diff.Plan, tableName string) error {
	for _, statement := range plan.Statements {
		parsed, err := sqlitequery.ParseStatement(statement.SQL)
		if err != nil {
			return fmt.Errorf("validate live diff statement %q: %w", statement.Source, err)
		}
		switch parsed := parsed.(type) {
		case *sqlitequery.CreateTableStatement:
			if sqliteIdentifierKey(parsed.Name.String()) != sqliteIdentifierKey(tableName) {
				return fmt.Errorf("diff-live target contains table %q, but -table selects %q", parsed.Name.String(), tableName)
			}
		case *sqlitequery.CreateIndexStatement:
			if sqliteIdentifierKey(parsed.Table.String()) != sqliteIdentifierKey(tableName) {
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
		parsed, actions, err := parseSource(source)
		if err != nil {
			return nil, fmt.Errorf("sqlite schema source %q: %w", source.Path, err)
		}
		for index, statement := range parsed.Statements {
			switch statement := statement.(type) {
			case *sqlitequery.CreateTableStatement:
				if err := snapshot.addTable(source.Path, statement, actions[index]); err != nil {
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
	for _, key := range sortedIndexKeys(snapshot.indexes) {
		index := snapshot.indexes[key]
		table := indexTableName(index.statement)
		if _, exists := snapshot.tables[qualifiedNameKey(table)]; !exists {
			return nil, fmt.Errorf("sqlite schema source %q defines index %s on missing table %s", index.source, displayName(index.statement.Name), displayName(table))
		}
	}
	if len(snapshot.tables) == 0 {
		return nil, fmt.Errorf("sqlite schema has no CREATE TABLE statements")
	}
	return snapshot, nil
}

type foreignKeyActions struct {
	onDelete string
	onUpdate string
}

type sqliteToken struct {
	text   string
	start  int
	end    int
	word   bool
	quoted bool
}

type sqliteSpan struct {
	start int
	end   int
}

func parseSource(source diff.Source) (*sqlitequery.Query, map[int][]foreignKeyActions, error) {
	withoutActions, actions := stripReferenceActions(source.SQL)
	parsed, err := sqlitequery.Parse(withoutActions)
	if err != nil {
		return nil, nil, err
	}

	actionsByStatement := make(map[int][]foreignKeyActions)
	actionIndex := 0
	for index, statement := range parsed.Statements {
		table, ok := statement.(*sqlitequery.CreateTableStatement)
		if !ok {
			continue
		}
		count := foreignKeyCount(table)
		if actionIndex+count > len(actions) {
			return nil, nil, fmt.Errorf("foreign-key action count does not match CREATE TABLE statements")
		}
		actionsByStatement[index] = append([]foreignKeyActions(nil), actions[actionIndex:actionIndex+count]...)
		actionIndex += count
	}
	if actionIndex != len(actions) {
		return nil, nil, fmt.Errorf("foreign-key action count does not match CREATE TABLE statements")
	}
	return parsed, actionsByStatement, nil
}

func foreignKeyCount(statement *sqlitequery.CreateTableStatement) int {
	count := 0
	for _, column := range statement.Columns {
		for _, constraint := range column.Constraints {
			if constraint.Kind == sqlitequery.ConstraintReferences {
				count++
			}
		}
	}
	for _, constraint := range statement.Constraints {
		if constraint.Kind == sqlitequery.ConstraintForeignKey {
			count++
		}
	}
	return count
}

func stripReferenceActions(source string) (string, []foreignKeyActions) {
	tokens := tokenizeSQLite(source)
	actions := make([]foreignKeyActions, 0)
	removals := make([]sqliteSpan, 0)
	parentheses := 0
	bodyParentheses := 0
	active := -1
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		switch token.text {
		case "(":
			parentheses++
			if bodyParentheses == 0 {
				bodyParentheses = parentheses
			}
		case ")":
			if parentheses > 0 {
				parentheses--
			}
		case ",":
			if bodyParentheses > 0 && parentheses == bodyParentheses {
				active = -1
			}
		case ";":
			parentheses = 0
			bodyParentheses = 0
			active = -1
		default:
			if !token.word {
				continue
			}
			if strings.EqualFold(token.text, "references") {
				actions = append(actions, foreignKeyActions{})
				active = len(actions) - 1
				continue
			}
			if active < 0 || !strings.EqualFold(token.text, "on") {
				continue
			}
			action, end, ok := parseReferenceAction(tokens, index)
			if !ok {
				continue
			}
			if action.kind == "DELETE" {
				actions[active].onDelete = action.value
			} else {
				actions[active].onUpdate = action.value
			}
			removals = append(removals, sqliteSpan{start: token.start, end: tokens[end].end})
			index = end
		}
	}
	if len(removals) == 0 {
		return source, actions
	}
	var result strings.Builder
	previous := 0
	for _, removal := range removals {
		result.WriteString(source[previous:removal.start])
		previous = removal.end
	}
	result.WriteString(source[previous:])
	return result.String(), actions
}

type parsedReferenceAction struct {
	kind  string
	value string
}

func parseReferenceAction(tokens []sqliteToken, index int) (parsedReferenceAction, int, bool) {
	if index+2 >= len(tokens) || !tokens[index+1].word {
		return parsedReferenceAction{}, index, false
	}
	kind := strings.ToUpper(tokens[index+1].text)
	if kind != "DELETE" && kind != "UPDATE" {
		return parsedReferenceAction{}, index, false
	}
	if index+3 >= len(tokens) || !tokens[index+2].word {
		return parsedReferenceAction{}, index, false
	}
	action := strings.ToUpper(tokens[index+2].text)
	end := index + 2
	switch action {
	case "CASCADE", "RESTRICT":
	case "NO":
		if index+3 >= len(tokens) || !tokens[index+3].word || !strings.EqualFold(tokens[index+3].text, "action") {
			return parsedReferenceAction{}, index, false
		}
		action = ""
		end++
	case "SET":
		if index+3 >= len(tokens) || !tokens[index+3].word {
			return parsedReferenceAction{}, index, false
		}
		value := strings.ToUpper(tokens[index+3].text)
		if value != "NULL" && value != "DEFAULT" {
			return parsedReferenceAction{}, index, false
		}
		action += " " + value
		end++
	default:
		return parsedReferenceAction{}, index, false
	}
	return parsedReferenceAction{kind: kind, value: action}, end, true
}

func tokenizeSQLite(source string) []sqliteToken {
	tokens := make([]sqliteToken, 0)
	for index := 0; index < len(source); {
		if source[index] == ' ' || source[index] == '\t' || source[index] == '\n' || source[index] == '\r' {
			index++
			continue
		}
		start := index
		switch source[index] {
		case '\'', '"', '`':
			quote := source[index]
			index++
			for index < len(source) {
				if source[index] != quote {
					index++
					continue
				}
				if index+1 < len(source) && source[index+1] == quote {
					index += 2
					continue
				}
				index++
				break
			}
			tokens = append(tokens, sqliteToken{text: source[start:index], start: start, end: index, quoted: true})
		case '[':
			index++
			for index < len(source) && source[index] != ']' {
				index++
			}
			if index < len(source) {
				index++
			}
			tokens = append(tokens, sqliteToken{text: source[start:index], start: start, end: index, quoted: true})
		case '-':
			if index+1 < len(source) && source[index+1] == '-' {
				index += 2
				for index < len(source) && source[index] != '\n' {
					index++
				}
				continue
			}
			index++
			tokens = append(tokens, sqliteToken{text: source[start:index], start: start, end: index})
		case '/':
			if index+1 < len(source) && source[index+1] == '*' {
				index += 2
				for index+1 < len(source) && (source[index] != '*' || source[index+1] != '/') {
					index++
				}
				if index+1 < len(source) {
					index += 2
				}
				continue
			}
			index++
			tokens = append(tokens, sqliteToken{text: source[start:index], start: start, end: index})
		default:
			if sqliteTokenWordStart(source[index]) {
				index++
				for index < len(source) && sqliteTokenWordPart(source[index]) {
					index++
				}
				tokens = append(tokens, sqliteToken{text: source[start:index], start: start, end: index, word: true})
				continue
			}
			index++
			tokens = append(tokens, sqliteToken{text: source[start:index], start: start, end: index})
		}
	}
	return tokens
}

func sqliteTokenWordStart(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func sqliteTokenWordPart(value byte) bool {
	return sqliteTokenWordStart(value) || value >= '0' && value <= '9' || value == '$'
}

// normalizeCreateTable puts SQLite primary keys and their implied nullability
// into the same representation regardless of whether the source used inline
// or table-level syntax. SQLite inspection renders this normalized form.
func normalizeCreateTable(statement *sqlitequery.CreateTableStatement) {
	var primaryKey sqlitequery.TableConstraint
	var hasPrimaryKey bool
	var inlineAutoincrement bool
	constraints := make([]sqlitequery.TableConstraint, 0, len(statement.Constraints)+1)
	for _, constraint := range statement.Constraints {
		if constraint.Kind != sqlitequery.ConstraintPrimaryKey {
			constraints = append(constraints, constraint)
			continue
		}
		if !hasPrimaryKey {
			primaryKey = constraint
			primaryKey.Name = nil
			hasPrimaryKey = true
		}
	}

	inlineColumns := make([]sqlitequery.IndexedColumn, 0)
	for columnIndex := range statement.Columns {
		column := &statement.Columns[columnIndex]
		columnConstraints := make([]sqlitequery.ColumnConstraint, 0, len(column.Constraints))
		for _, constraint := range column.Constraints {
			switch constraint.Kind {
			case sqlitequery.ConstraintReferences:
				constraints = append(constraints, sqlitequery.TableConstraint{
					Name: constraint.Name,
					Kind: sqlitequery.ConstraintForeignKey,
					Columns: []sqlitequery.IndexedColumn{{
						Expression: &sqlitequery.IdentifierExpression{Name: sqlitequery.QualifiedName{column.Name}},
					}},
					References: constraint.References,
				})
				continue
			case sqlitequery.ConstraintUnique:
				constraints = append(constraints, sqlitequery.TableConstraint{
					Name:     constraint.Name,
					Kind:     sqlitequery.ConstraintUnique,
					Columns:  []sqlitequery.IndexedColumn{{Expression: &sqlitequery.IdentifierExpression{Name: sqlitequery.QualifiedName{column.Name}}}},
					Conflict: constraint.Conflict,
				})
				continue
			case sqlitequery.ConstraintCheck:
				constraints = append(constraints, sqlitequery.TableConstraint{
					Name:       constraint.Name,
					Kind:       sqlitequery.ConstraintCheck,
					Expression: constraint.Expression,
				})
				continue
			}
			if constraint.Kind != sqlitequery.ConstraintPrimaryKey {
				columnConstraints = append(columnConstraints, constraint)
				continue
			}
			inlineColumns = append(inlineColumns, sqlitequery.IndexedColumn{
				Expression: &sqlitequery.IdentifierExpression{Name: sqlitequery.QualifiedName{column.Name}},
				Direction:  constraint.Direction,
			})
			if constraint.Autoincrement {
				columnConstraints = append(columnConstraints, constraint)
				inlineAutoincrement = true
			}
			if !hasPrimaryKey {
				primaryKey = sqlitequery.TableConstraint{
					Kind:     sqlitequery.ConstraintPrimaryKey,
					Conflict: constraint.Conflict,
					Columns:  append([]sqlitequery.IndexedColumn(nil), inlineColumns...),
				}
				hasPrimaryKey = true
			}
		}
		sort.SliceStable(columnConstraints, func(left, right int) bool {
			return columnConstraints[left].Kind < columnConstraints[right].Kind
		})
		column.Constraints = columnConstraints
	}
	if len(inlineColumns) > 0 && !hasPrimaryKey {
		primaryKey = sqlitequery.TableConstraint{
			Kind:    sqlitequery.ConstraintPrimaryKey,
			Columns: inlineColumns,
		}
		hasPrimaryKey = true
	}
	if hasPrimaryKey {
		primaryKey.Name = nil
		for index := range statement.Columns {
			column := &statement.Columns[index]
			if !primaryKeyContainsColumn(primaryKey, column.Name.Name) || hasColumnConstraint(*column, sqlitequery.ConstraintNotNull) {
				continue
			}
			column.Constraints = append(column.Constraints, sqlitequery.ColumnConstraint{Kind: sqlitequery.ConstraintNotNull})
		}
		if !inlineAutoincrement {
			constraints = append(constraints, primaryKey)
		}
	}
	statement.Constraints = constraints
}

func primaryKeyContainsColumn(constraint sqlitequery.TableConstraint, name string) bool {
	for _, column := range constraint.Columns {
		expression, ok := column.Expression.(*sqlitequery.IdentifierExpression)
		if ok && len(expression.Name) == 1 && sqliteIdentifierKey(expression.Name[0].Name) == sqliteIdentifierKey(name) {
			return true
		}
	}
	return false
}

func hasColumnConstraint(column sqlitequery.ColumnDefinition, kind sqlitequery.ConstraintKind) bool {
	for _, constraint := range column.Constraints {
		if constraint.Kind == kind {
			return true
		}
	}
	return false
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

	comparison := diff.CompareSchemas(
		diff.Schema[tableDefinition, indexDefinition]{Tables: baseline.tables, Indexes: baseline.indexes},
		diff.Schema[tableDefinition, indexDefinition]{Tables: target.tables, Indexes: target.indexes},
		func(left, right tableDefinition) bool {
			return ast.Equal(left.normalized, right.normalized) && sameTableConstraints(left, right)
		},
		func(left, right indexDefinition) bool { return sameIndex(left.statement, right.statement) },
	)
	generated := make([]generatedStatement, 0)
	diagnostics := make([]string, 0)
	for _, entry := range comparison.Tables.Added {
		statement, err := createTableStatement(entry.Value)
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
		statement, err := createIndexStatement(entry.Value.statement)
		if err != nil {
			return diff.Plan{}, err
		}
		generated = append(generated, statement)
	}
	for _, pair := range comparison.Indexes.Matched {
		if !pair.Equal {
			diagnostics = append(diagnostics, fmt.Sprintf("index %s changed", displayName(pair.Target.statement.Name)))
		}
	}
	for _, entry := range comparison.Indexes.Removed {
		diagnostics = append(diagnostics, fmt.Sprintf("index %s was removed", displayName(entry.Value.statement.Name)))
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
	source      string
	statement   *sqlitequery.CreateTableStatement
	normalized  *sqlitequery.CreateTableStatement
	foreignKeys []foreignKeyActions
}

func (s *schemaSnapshot) addTable(source string, statement *sqlitequery.CreateTableStatement, foreignKeys []foreignKeyActions) error {
	if statement.As != nil {
		return fmt.Errorf("sqlite schema source %q contains CREATE TABLE AS SELECT for %s, which has no declared table shape", source, displayName(statement.Name))
	}
	key := qualifiedNameKey(statement.Name)
	if previous, exists := s.tables[key]; exists {
		return fmt.Errorf("sqlite schema source %q defines table %s already defined by %q", source, displayName(statement.Name), previous.source)
	}
	columns := make(map[string]struct{}, len(statement.Columns))
	for _, column := range statement.Columns {
		key := sqliteIdentifierKey(column.Name.Name)
		if _, exists := columns[key]; exists {
			return fmt.Errorf("sqlite schema source %q defines duplicate column %q in table %s", source, column.Name.Name, displayName(statement.Name))
		}
		columns[key] = struct{}{}
	}
	normalized := *statement
	normalized.Columns = append([]sqlitequery.ColumnDefinition(nil), statement.Columns...)
	for index := range normalized.Columns {
		normalized.Columns[index].Constraints = append([]sqlitequery.ColumnConstraint(nil), statement.Columns[index].Constraints...)
	}
	normalized.Constraints = append([]sqlitequery.TableConstraint(nil), statement.Constraints...)
	normalizeCreateTable(&normalized)
	s.tables[key] = tableDefinition{
		source:      source,
		statement:   statement,
		normalized:  &normalized,
		foreignKeys: append([]foreignKeyActions(nil), foreignKeys...),
	}
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

func createTableStatement(table tableDefinition) (generatedStatement, error) {
	copy := *table.statement
	copy.IfNotExists = false
	sql, err := serializeWithReferenceActions(&copy, table.foreignKeys)
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

func diffTable(baseline tableDefinition, target tableDefinition) ([]generatedStatement, []string, error) {
	generated := make([]generatedStatement, 0)
	diagnostics := make([]string, 0)
	baselineColumns := make(map[string]sqlitequery.ColumnDefinition, len(baseline.normalized.Columns))
	for _, column := range baseline.normalized.Columns {
		baselineColumns[sqliteIdentifierKey(column.Name.Name)] = column
	}
	targetColumns := make(map[string]sqlitequery.ColumnDefinition, len(target.normalized.Columns))
	for _, column := range target.normalized.Columns {
		targetColumns[sqliteIdentifierKey(column.Name.Name)] = column
	}
	addedColumns := make(map[string]struct{})
	for key := range targetColumns {
		if _, exists := baselineColumns[key]; !exists {
			addedColumns[key] = struct{}{}
		}
	}
	if baseline.normalized.Persistence != target.normalized.Persistence {
		diagnostics = append(diagnostics, fmt.Sprintf("table %s persistence changed", displayName(target.normalized.Name)))
	}
	if !ast.Equal(baseline.normalized.Options, target.normalized.Options) {
		diagnostics = append(diagnostics, fmt.Sprintf("table %s options changed", displayName(target.normalized.Name)))
	}
	if !sameTableConstraintsForAdditions(baseline, target, addedColumns) {
		diagnostics = append(diagnostics, fmt.Sprintf("table %s constraints changed", displayName(target.normalized.Name)))
	}

	for _, column := range target.normalized.Columns {
		key := sqliteIdentifierKey(column.Name.Name)
		previous, exists := baselineColumns[key]
		if !exists {
			if diagnostic := columnAddDiagnostic(targetColumn(target, column.Name.Name)); diagnostic != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("new column %s.%s %s", displayName(target.normalized.Name), column.Name.Name, diagnostic))
				continue
			}
			addedColumn := targetColumn(target, column.Name.Name)
			statement := &sqlitequery.AlterTableStatement{
				Name: target.normalized.Name,
				Action: sqlitequery.AlterTableAction{
					Kind:   sqlitequery.AlterTableAddColumn,
					Column: &addedColumn,
				},
			}
			sql, err := serializeWithReferenceActions(statement, foreignKeyActionsForColumn(target, column.Name.Name))
			if err != nil {
				return nil, nil, err
			}
			name := displayName(target.normalized.Name)
			generated = append(generated, generatedStatement{
				name:    "add_column_" + filenamePart(name) + "_" + filenamePart(column.Name.Name),
				sql:     sql,
				summary: "add column " + name + "." + column.Name.Name,
			})
			continue
		}
		if !sameColumn(previous, column) {
			diagnostics = append(diagnostics, fmt.Sprintf("column %s.%s changed", displayName(target.normalized.Name), column.Name.Name))
		}
	}
	for _, column := range baseline.normalized.Columns {
		if _, exists := targetColumns[sqliteIdentifierKey(column.Name.Name)]; !exists {
			diagnostics = append(diagnostics, fmt.Sprintf("column %s.%s was removed", displayName(baseline.normalized.Name), column.Name.Name))
		}
	}
	return generated, diagnostics, nil
}

func targetColumn(table tableDefinition, name string) sqlitequery.ColumnDefinition {
	for _, column := range table.statement.Columns {
		if sqliteIdentifierKey(column.Name.Name) == sqliteIdentifierKey(name) {
			return column
		}
	}
	return sqlitequery.ColumnDefinition{Name: sqlitequery.Identifier{Name: name}}
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

type tableConstraint struct {
	constraint sqlitequery.TableConstraint
	action     foreignKeyActions
}

func sameTableConstraints(left, right tableDefinition) bool {
	return sameTableConstraintLists(canonicalTableConstraints(left), canonicalTableConstraints(right))
}

func sameTableConstraintsForAdditions(left, right tableDefinition, addedColumns map[string]struct{}) bool {
	rightConstraints := canonicalTableConstraints(right)
	inlineForeignKeys := addedInlineForeignKeyConstraints(right, addedColumns)
	if len(inlineForeignKeys) > 0 {
		filtered := make([]tableConstraint, 0, len(rightConstraints))
		for _, constraint := range rightConstraints {
			if constraint.constraint.Kind == sqlitequery.ConstraintForeignKey && containsTableConstraint(inlineForeignKeys, constraint.constraint) {
				continue
			}
			filtered = append(filtered, constraint)
		}
		rightConstraints = filtered
	}
	return sameTableConstraintLists(canonicalTableConstraints(left), rightConstraints)
}

func sameTableConstraintLists(leftConstraints, rightConstraints []tableConstraint) bool {
	if len(leftConstraints) != len(rightConstraints) {
		return false
	}

	matched := make([]bool, len(rightConstraints))
	for _, leftConstraint := range leftConstraints {
		found := false
		for index, rightConstraint := range rightConstraints {
			if matched[index] || leftConstraint.action != rightConstraint.action || !ast.Equal(canonicalizeTableConstraint(leftConstraint.constraint), canonicalizeTableConstraint(rightConstraint.constraint)) {
				continue
			}
			matched[index] = true
			found = true
			break
		}
		if !found {
			return false
		}
	}
	return true
}

func addedInlineForeignKeyConstraints(table tableDefinition, addedColumns map[string]struct{}) []sqlitequery.TableConstraint {
	constraints := make([]sqlitequery.TableConstraint, 0)
	for _, column := range table.statement.Columns {
		if _, ok := addedColumns[sqliteIdentifierKey(column.Name.Name)]; !ok {
			continue
		}
		for _, constraint := range column.Constraints {
			if constraint.Kind != sqlitequery.ConstraintReferences {
				continue
			}
			constraints = append(constraints, sqlitequery.TableConstraint{
				Name: constraint.Name,
				Kind: sqlitequery.ConstraintForeignKey,
				Columns: []sqlitequery.IndexedColumn{{
					Expression: &sqlitequery.IdentifierExpression{Name: sqlitequery.QualifiedName{column.Name}},
				}},
				References: constraint.References,
			})
		}
	}
	return constraints
}

func containsTableConstraint(constraints []sqlitequery.TableConstraint, target sqlitequery.TableConstraint) bool {
	for _, constraint := range constraints {
		if ast.Equal(canonicalizeTableConstraint(constraint), canonicalizeTableConstraint(target)) {
			return true
		}
	}
	return false
}

func canonicalTableConstraints(table tableDefinition) []tableConstraint {
	constraints := make([]tableConstraint, len(table.normalized.Constraints))
	for index, constraint := range table.normalized.Constraints {
		constraints[index] = tableConstraint{constraint: constraint}
	}

	foreignKeys := tableForeignKeyConstraints(table)
	used := make([]bool, len(foreignKeys))
	for index := range constraints {
		if constraints[index].constraint.Kind != sqlitequery.ConstraintForeignKey {
			continue
		}
		for foreignKeyIndex, foreignKey := range foreignKeys {
			if used[foreignKeyIndex] || !ast.Equal(constraints[index].constraint, foreignKey.constraint) {
				continue
			}
			constraints[index].action = foreignKey.action
			used[foreignKeyIndex] = true
			break
		}
	}
	return constraints
}

func tableForeignKeyConstraints(table tableDefinition) []tableConstraint {
	constraints := make([]tableConstraint, 0)
	actionIndex := 0
	for _, column := range table.statement.Columns {
		for _, constraint := range column.Constraints {
			if constraint.Kind != sqlitequery.ConstraintReferences {
				continue
			}
			foreignKey := sqlitequery.TableConstraint{
				Name: constraint.Name,
				Kind: sqlitequery.ConstraintForeignKey,
				Columns: []sqlitequery.IndexedColumn{{
					Expression: &sqlitequery.IdentifierExpression{Name: sqlitequery.QualifiedName{column.Name}},
				}},
				References: constraint.References,
			}
			constraints = append(constraints, tableConstraint{
				constraint: foreignKey,
				action:     table.foreignKeys[actionIndex],
			})
			actionIndex++
		}
	}
	for _, constraint := range table.statement.Constraints {
		if constraint.Kind != sqlitequery.ConstraintForeignKey {
			continue
		}
		constraints = append(constraints, tableConstraint{
			constraint: constraint,
			action:     table.foreignKeys[actionIndex],
		})
		actionIndex++
	}
	return constraints
}

func foreignKeyActionsForColumn(table tableDefinition, columnName string) []foreignKeyActions {
	actions := make([]foreignKeyActions, 0)
	index := 0
	for _, column := range table.statement.Columns {
		for _, constraint := range column.Constraints {
			if constraint.Kind != sqlitequery.ConstraintReferences {
				continue
			}
			if sqliteIdentifierKey(column.Name.Name) == sqliteIdentifierKey(columnName) && index < len(table.foreignKeys) {
				actions = append(actions, table.foreignKeys[index])
			}
			index++
		}
	}
	return actions
}

func sameIndex(left *sqlitequery.CreateIndexStatement, right *sqlitequery.CreateIndexStatement) bool {
	leftCopy := *left
	leftCopy.IfNotExists = false
	canonicalizeIndex(&leftCopy)
	rightCopy := *right
	rightCopy.IfNotExists = false
	canonicalizeIndex(&rightCopy)
	return ast.Equal(leftCopy, rightCopy)
}

func sameColumn(left, right sqlitequery.ColumnDefinition) bool {
	return ast.Equal(canonicalizeColumn(left), canonicalizeColumn(right))
}

func canonicalizeColumn(column sqlitequery.ColumnDefinition) sqlitequery.ColumnDefinition {
	canonical := column
	canonical.Name.Name = sqliteIdentifierKey(canonical.Name.Name)
	canonical.Type.Words = canonicalizeWords(canonical.Type.Words)
	canonical.Type.Modifiers = canonicalizeExpressions(canonical.Type.Modifiers)
	canonical.Constraints = make([]sqlitequery.ColumnConstraint, len(column.Constraints))
	for index, constraint := range column.Constraints {
		canonical.Constraints[index] = canonicalizeColumnConstraint(constraint)
	}
	return canonical
}

func canonicalizeColumnConstraint(constraint sqlitequery.ColumnConstraint) sqlitequery.ColumnConstraint {
	canonical := constraint
	canonical.Name = canonicalizeIdentifier(canonical.Name)
	canonical.Expression = canonicalizeExpression(canonical.Expression)
	canonical.References = canonicalizeReference(canonical.References)
	canonical.Collation = canonicalizeIdentifier(canonical.Collation)
	if canonical.Generated != nil {
		generated := *canonical.Generated
		generated.Expression = canonicalizeExpression(generated.Expression)
		canonical.Generated = &generated
	}
	return canonical
}

func canonicalizeTableConstraint(constraint sqlitequery.TableConstraint) sqlitequery.TableConstraint {
	canonical := constraint
	canonical.Name = canonicalizeIdentifier(canonical.Name)
	canonical.Columns = canonicalizeIndexedColumns(canonical.Columns)
	canonical.Expression = canonicalizeExpression(canonical.Expression)
	canonical.References = canonicalizeReference(canonical.References)
	return canonical
}

func canonicalizeIndexedColumns(columns []sqlitequery.IndexedColumn) []sqlitequery.IndexedColumn {
	canonical := make([]sqlitequery.IndexedColumn, len(columns))
	for index, column := range columns {
		canonical[index] = column
		canonical[index].Expression = canonicalizeExpression(column.Expression)
		canonical[index].Collation = canonicalizeIdentifier(column.Collation)
	}
	return canonical
}

func canonicalizeReference(reference *sqlitequery.Reference) *sqlitequery.Reference {
	if reference == nil {
		return nil
	}
	canonical := *reference
	canonical.Table = canonicalizeQualifiedName(canonical.Table)
	canonical.Columns = make([]sqlitequery.Identifier, len(reference.Columns))
	for index, column := range reference.Columns {
		canonical.Columns[index] = canonicalizeIdentifierValue(column)
	}
	return &canonical
}

func canonicalizeIdentifier(identifier *sqlitequery.Identifier) *sqlitequery.Identifier {
	if identifier == nil {
		return nil
	}
	canonical := *identifier
	canonical.Name = sqliteIdentifierKey(canonical.Name)
	return &canonical
}

func canonicalizeIdentifierValue(identifier sqlitequery.Identifier) sqlitequery.Identifier {
	identifier.Name = sqliteIdentifierKey(identifier.Name)
	return identifier
}

func canonicalizeExpressions(expressions []sqlitequery.Expression) []sqlitequery.Expression {
	canonical := make([]sqlitequery.Expression, len(expressions))
	for index, expression := range expressions {
		canonical[index] = canonicalizeExpression(expression)
	}
	return canonical
}

func canonicalizeWords(words []string) []string {
	canonical := make([]string, len(words))
	for index, word := range words {
		canonical[index] = strings.ToLower(word)
	}
	return canonical
}

func canonicalizeExpression(expression sqlitequery.Expression) sqlitequery.Expression {
	switch expression := expression.(type) {
	case *sqlitequery.IdentifierExpression:
		if expression == nil {
			return nil
		}
		canonical := *expression
		canonical.Name = canonicalizeQualifiedName(canonical.Name)
		return &canonical
	case *sqlitequery.StarExpression:
		if expression == nil {
			return nil
		}
		canonical := *expression
		canonical.Qualifier = canonicalizeQualifiedName(canonical.Qualifier)
		return &canonical
	case *sqlitequery.UnaryExpression:
		if expression == nil {
			return nil
		}
		canonical := *expression
		canonical.Expression = canonicalizeExpression(expression.Expression)
		return &canonical
	case *sqlitequery.BinaryExpression:
		if expression == nil {
			return nil
		}
		canonical := *expression
		canonical.Left = canonicalizeExpression(expression.Left)
		canonical.Right = canonicalizeExpression(expression.Right)
		return &canonical
	case *sqlitequery.CallExpression:
		if expression == nil {
			return nil
		}
		canonical := *expression
		canonical.Function = canonicalizeQualifiedName(expression.Function)
		canonical.Arguments = canonicalizeExpressions(expression.Arguments)
		return &canonical
	default:
		return expression
	}
}

func canonicalizeIndex(statement *sqlitequery.CreateIndexStatement) {
	statement.Name = canonicalizeQualifiedName(statement.Name)
	statement.Table = canonicalizeQualifiedName(statement.Table)
	statement.Elements = canonicalizeIndexedColumns(statement.Elements)
	statement.Where = canonicalizeExpression(statement.Where)
}

func canonicalizeQualifiedName(name sqlitequery.QualifiedName) sqlitequery.QualifiedName {
	canonical := append(sqlitequery.QualifiedName(nil), name...)
	for index := range canonical {
		canonical[index].Name = sqliteIdentifierKey(canonical[index].Name)
	}
	return canonical
}

func serializeWithReferenceActions(statement sqlitequery.Statement, actions []foreignKeyActions) (string, error) {
	serialized, err := serialize(statement)
	if err != nil || len(actions) == 0 {
		return serialized, err
	}
	tokens := tokenizeSQLite(serialized)
	insertions := make([]struct {
		position int
		value    string
	}, 0, len(actions))
	actionIndex := 0
	for index, token := range tokens {
		if !token.word || !strings.EqualFold(token.text, "references") {
			continue
		}
		if actionIndex >= len(actions) {
			return "", fmt.Errorf("sqlite schema diff: foreign-key action count does not match serialized statement")
		}
		end, ok := referenceEnd(tokens, index+1)
		if !ok {
			return "", fmt.Errorf("sqlite schema diff: cannot locate the end of a REFERENCES clause")
		}
		value := referenceActionSQL(actions[actionIndex])
		if value != "" {
			insertions = append(insertions, struct {
				position int
				value    string
			}{position: tokens[end].end, value: value})
		}
		actionIndex++
	}
	if actionIndex != len(actions) {
		return "", fmt.Errorf("sqlite schema diff: foreign-key action count does not match serialized statement")
	}
	if len(insertions) == 0 {
		return serialized, nil
	}
	var result strings.Builder
	previous := 0
	for _, insertion := range insertions {
		result.WriteString(serialized[previous:insertion.position])
		result.WriteString(insertion.value)
		previous = insertion.position
	}
	result.WriteString(serialized[previous:])
	return result.String(), nil
}

func referenceEnd(tokens []sqliteToken, index int) (int, bool) {
	if index >= len(tokens) || !sqliteNameToken(tokens[index]) {
		return 0, false
	}
	end := index
	for end+2 < len(tokens) && tokens[end+1].text == "." && sqliteNameToken(tokens[end+2]) {
		end += 2
	}
	if end+1 < len(tokens) && tokens[end+1].text == "(" {
		depth := 0
		for current := end + 1; current < len(tokens); current++ {
			switch tokens[current].text {
			case "(":
				depth++
			case ")":
				depth--
				if depth == 0 {
					return current, true
				}
			}
		}
		return 0, false
	}
	return end, true
}

func sqliteNameToken(token sqliteToken) bool {
	return token.word || token.quoted
}

func referenceActionSQL(actions foreignKeyActions) string {
	var result strings.Builder
	if actions.onDelete != "" {
		result.WriteString(" ON DELETE ")
		result.WriteString(actions.onDelete)
	}
	if actions.onUpdate != "" {
		result.WriteString(" ON UPDATE ")
		result.WriteString(actions.onUpdate)
	}
	return result.String()
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

func qualifiedNameKey(name sqlitequery.QualifiedName) string {
	var key strings.Builder
	for _, part := range name {
		value := sqliteIdentifierKey(part.Name)
		fmt.Fprintf(&key, "%d:%s", len(value), value)
	}
	return key.String()
}

func indexTableName(index *sqlitequery.CreateIndexStatement) sqlitequery.QualifiedName {
	if len(index.Table) != 1 || len(index.Name) < 2 {
		return index.Table
	}
	table := make(sqlitequery.QualifiedName, 0, len(index.Name))
	table = append(table, index.Name[:len(index.Name)-1]...)
	return append(table, index.Table...)
}

func sqliteIdentifierKey(value string) string {
	key := []byte(value)
	for index, character := range key {
		if character >= 'A' && character <= 'Z' {
			key[index] += 'a' - 'A'
		}
	}
	return string(key)
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
