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
		normalizedSource, facts, err := scanPostgreSQLClauses(string(source.SQL))
		if err != nil {
			return nil, fmt.Errorf("postgresql schema source %q: %w", source.Path, err)
		}
		parsed, err := pgquery.Parse(normalizedSource)
		if err != nil {
			return nil, fmt.Errorf("postgresql schema source %q: %w", source.Path, err)
		}
		consumed := make(map[string]struct{})
		for index, statement := range parsed.Statements {
			switch statement := statement.(type) {
			case *pgquery.CreateTableStatement:
				tableKey := qualifiedNameKey(statement.Name)
				tableFacts, ok := facts.tables[tableKey]
				if !ok {
					return nil, fmt.Errorf("postgresql schema source %q table %s has no scanned facts", source.Path, displayName(statement.Name))
				}
				identities, foreignKeys, err := attachFacts(source.Path, statement, tableKey, tableFacts)
				if err != nil {
					return nil, err
				}
				consumed[tableKey] = struct{}{}
				if err := snapshot.addTable(source.Path, statement, identities, foreignKeys); err != nil {
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
		if err := rejectUnconsumedTables(source.Path, facts, consumed); err != nil {
			return nil, err
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

func rejectUnconsumedTables(source string, facts scannedPostgreSQLFacts, consumed map[string]struct{}) error {
	for key := range facts.tables {
		if _, ok := consumed[key]; !ok {
			return fmt.Errorf("postgresql schema source %q scanned table %s was not parsed", source, key)
		}
	}
	return nil
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
		func(left, right tableDefinition) bool { return sameTableDefinition(left, right) },
		func(left, right indexDefinition) bool { return sameIndex(left.statement, right.statement) },
	)
	entries := make([]loweringEntry, 0)
	diagnostics := make([]string, 0)
	decisions := make([]diff.RequiredDecision, 0)
	for _, entry := range comparison.Tables.Added {
		created, err := createTableEntry(entry.Value)
		if err != nil {
			return diff.Plan{}, err
		}
		entries = append(entries, created)
	}
	for _, pair := range comparison.Tables.Matched {
		tableEntries, tableDiagnostics, tableDecisions, err := diffTable(pair.Baseline, pair.Target)
		if err != nil {
			return diff.Plan{}, err
		}
		entries = append(entries, tableEntries...)
		diagnostics = append(diagnostics, tableDiagnostics...)
		decisions = append(decisions, tableDecisions...)
	}
	for _, entry := range comparison.Tables.Removed {
		diagnostics = append(diagnostics, fmt.Sprintf("table %s was removed", displayName(entry.Value.statement.Name)))
	}

	for _, entry := range comparison.Indexes.Added {
		if entry.Value.statement.Concurrently {
			diagnostics = append(diagnostics, fmt.Sprintf("index %s uses CONCURRENTLY, which needs non-transactional migration support", displayName(*entry.Value.statement.Name)))
			continue
		}
		created, err := createIndexEntry(entry.Value)
		if err != nil {
			return diff.Plan{}, err
		}
		entries = append(entries, created)
	}
	for _, pair := range comparison.Indexes.Matched {
		if !sameIndex(pair.Baseline.statement, pair.Target.statement) {
			diagnostics = append(diagnostics, fmt.Sprintf("index %s changed", displayName(*pair.Target.statement.Name)))
		}
	}
	for _, entry := range comparison.Indexes.Removed {
		diagnostics = append(diagnostics, fmt.Sprintf("index %s was removed", displayName(*entry.Value.statement.Name)))
	}
	remaining := append([]string(nil), diagnostics...)
	if len(remaining) > 0 {
		return diff.Plan{}, manualMigrationError(remaining)
	}
	if len(decisions) == 0 {
		decisions = nil
	}
	if len(entries) == 0 {
		return diff.Plan{Dialect: "postgresql"}, nil
	}
	if err := validateLoweringSources(entries); err != nil {
		return diff.Plan{}, err
	}
	baseOperations := previewOperations(entries)
	owned, err := cloneLoweringEntries(entries)
	if err != nil {
		return diff.Plan{}, err
	}
	ownedDecisions := append([]diff.RequiredDecision(nil), decisions...)
	return diff.NewPlan("postgresql", baseOperations, ownedDecisions, func(resolutions map[string]diff.Resolution) (diff.LoweringResult, error) {
		// Every value captured by this closure is owned by the plan. Resolve may be
		// called repeatedly, so start from fresh copies on every invocation.
		run, err := cloneLoweringEntries(owned)
		if err != nil {
			return diff.LoweringResult{}, err
		}
		for index := range run {
			if run[index].decisionID == "" {
				continue
			}
			decision, ok := decisionByID(ownedDecisions, run[index].decisionID)
			if !ok {
				return diff.LoweringResult{}, fmt.Errorf("postgresql schema diff: missing decision %s", run[index].decisionID)
			}
			if _, ok := resolutions[decision.ID]; !ok {
				return diff.LoweringResult{}, fmt.Errorf("postgresql schema diff: missing resolution %s", decision.ID)
			}
			table := run[index].target
			if table.statement == nil {
				return diff.LoweringResult{}, fmt.Errorf("postgresql schema diff: missing table %s", decision.Table)
			}
			switch decision.Kind {
			case diff.DecisionRename:
				_, _, err := renameIdentifiersFromEntry(run[index], decision)
				if err != nil {
					return diff.LoweringResult{}, err
				}
				run[index].action = lowerRenameColumn
				run[index].operation.Column = decision.Target
			case diff.DecisionBackfill:
				if run[index].targetColumn == nil {
					return diff.LoweringResult{}, fmt.Errorf("postgresql schema diff: missing backfill column %s", decision.Column)
				}
				run[index].action = lowerBackfill
			}
		}
		irreversible := ""
		for _, decision := range ownedDecisions {
			if decision.Kind == diff.DecisionBackfill {
				irreversible = "caller-supplied backfill has no inferred reverse"
				break
			}
		}
		operations := make([]diff.ProposedOperation, 0, len(run))
		var statements []diff.PlannedStatement
		for index := range run {
			operation, err := lowerPostgreSQLEntry(run[index], resolutions)
			if err != nil {
				return diff.LoweringResult{}, err
			}
			operations = append(operations, operation)
		}
		actions := make([]loweringAction, len(run))
		for index := range run {
			actions[index] = run[index].action
		}
		statements = scheduleLoweredOperations(operations, actions)
		return diff.LoweringResult{Operations: operations, Statements: statements, IrreversibleReason: irreversible}, nil
	})
}

func validateLoweringSources(entries []loweringEntry) error {
	seen := make(map[string]string, len(entries))
	for _, entry := range entries {
		prefix := "add_column_"
		if entry.action == lowerCreateTable {
			prefix = "create_table_"
		}
		if entry.action == lowerCreateIndex {
			prefix = "create_index_"
		}
		if entry.action == lowerAlterNullability {
			prefix = "alter_nullability_"
		}
		if entry.action == lowerReplaceConstraint {
			prefix = "replace_constraint_"
		}
		source := prefix + filenamePart(entry.operation.Table)
		if entry.action == lowerAddColumn || entry.action == lowerBackfill {
			source += "_" + filenamePart(entry.operation.Column)
		}
		if entry.action == lowerReplaceConstraint {
			source += "_" + filenamePart(entry.operation.Constraint)
		}
		source += ".sql"
		if previous, ok := seen[source]; ok {
			return fmt.Errorf("migrate diff: duplicate generated SQL source %q for %q and %q", source, previous, entry.operation.Summary)
		}
		seen[source] = entry.operation.Summary
	}
	return nil
}

func decisionByID(decisions []diff.RequiredDecision, id string) (diff.RequiredDecision, bool) {
	for _, decision := range decisions {
		if decision.ID == id {
			return decision, true
		}
	}
	return diff.RequiredDecision{}, false
}

//nolint:unused // Kept as a package-local compatibility path.
func renameIdentifiers(table pgquery.CreateTableStatement, decision diff.RequiredDecision) (string, string, error) {
	for _, column := range table.Columns {
		if column.Name.Name == decision.Target {
			return renderIdentifier(pgquery.Identifier{Name: decision.Baseline, Quoted: column.Name.Quoted}), renderIdentifier(column.Name), nil
		}
	}
	return "", "", fmt.Errorf("postgresql schema diff: rename target %s.%s is missing", decision.Table, decision.Target)
}

//nolint:unused // Kept as a package-local compatibility path.
func tableColumn(table tableDefinition, name string) (pgquery.ColumnDefinition, error) {
	for _, column := range table.statement.Columns {
		if column.Name.Name == name {
			return column, nil
		}
	}
	return pgquery.ColumnDefinition{}, fmt.Errorf("postgresql schema diff: column %s.%s is missing", displayName(table.statement.Name), name)
}

func findColumn(table *pgquery.CreateTableStatement, name string) *pgquery.ColumnDefinition {
	if table == nil {
		return nil
	}
	for index := range table.Columns {
		if table.Columns[index].Name.Name == name {
			return &table.Columns[index]
		}
	}
	return nil
}

func addColumnSQL(table tableDefinition, column pgquery.ColumnDefinition) (string, error) {
	definition, err := renderColumnDefinition(table, column)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;\n", reverseName(table.statement.Name), definition), nil
}

func sameTableDefinition(left, right tableDefinition) bool {
	if !ast.Equal(left.statement, right.statement) || !equalIdentityFacts(left.identities, right.identities) || !equalForeignKeyActions(left.foreignKeys, right.foreignKeys) {
		return false
	}
	return true
}

func identityFor(table tableDefinition, column pgquery.Identifier) (identityMode, bool) {
	mode, ok := table.identities[identifierKey(column)]
	return mode, ok
}

func sameColumnDefinition(leftTable tableDefinition, left pgquery.ColumnDefinition, rightTable tableDefinition, right pgquery.ColumnDefinition) bool {
	return ast.Equal(normalizedColumn(left, false), normalizedColumn(right, false)) && sameIdentity(leftTable, left.Name, rightTable, right.Name)
}

func sameIdentity(leftTable tableDefinition, left pgquery.Identifier, rightTable tableDefinition, right pgquery.Identifier) bool {
	leftMode, leftOK := identityFor(leftTable, left)
	rightMode, rightOK := identityFor(rightTable, right)
	return leftOK == rightOK && (!leftOK || leftMode == rightMode)
}

func sameColumnExceptNullability(leftTable tableDefinition, left pgquery.ColumnDefinition, rightTable tableDefinition, right pgquery.ColumnDefinition) bool {
	left = withoutNullability(left)
	right = withoutNullability(right)
	return ast.Equal(normalizedColumn(left, false), normalizedColumn(right, false)) && sameIdentity(leftTable, left.Name, rightTable, right.Name)
}

func withoutNullability(column pgquery.ColumnDefinition) pgquery.ColumnDefinition {
	cloned := column
	cloned.Constraints = make([]pgquery.ColumnConstraint, 0, len(column.Constraints))
	for _, constraint := range column.Constraints {
		if constraint.Kind == pgquery.ConstraintNull || constraint.Kind == pgquery.ConstraintNotNull {
			continue
		}
		cloned.Constraints = append(cloned.Constraints, constraint)
	}
	return cloned
}

func namedTableConstraints(table tableDefinition) (map[string]pgquery.TableConstraint, error) {
	result := make(map[string]pgquery.TableConstraint, len(table.statement.Constraints))
	for _, constraint := range table.statement.Constraints {
		if constraint.Name == nil {
			continue
		}
		key := identifierKey(*constraint.Name)
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("postgresql schema diff: table %s has duplicate constraint %s", displayName(table.statement.Name), displayName(pgquery.QualifiedName{*constraint.Name}))
		}
		result[key] = constraint
	}
	return result, nil
}

func foreignKeyActionsFor(table tableDefinition, constraint pgquery.TableConstraint) (foreignKeyActions, bool) {
	if constraint.Name == nil {
		return foreignKeyActions{}, false
	}
	key := foreignKeyKey{table: qualifiedNameKey(table.statement.Name), constraint: identifierKey(*constraint.Name)}
	actions, ok := table.foreignKeys[key]
	return actions, ok
}

func sameNamedConstraint(leftTable tableDefinition, left pgquery.TableConstraint, rightTable tableDefinition, right pgquery.TableConstraint) bool {
	if !ast.Equal(left, right) {
		return false
	}
	leftActions, leftHas := foreignKeyActionsFor(leftTable, left)
	rightActions, rightHas := foreignKeyActionsFor(rightTable, right)
	return leftHas == rightHas && (!leftHas || leftActions == rightActions)
}

func columnNullable(column pgquery.ColumnDefinition) bool {
	return !hasColumnConstraint(column.Constraints, pgquery.ConstraintNotNull)
}

func identityModeName(mode identityMode, present bool) string {
	if !present {
		return "absent"
	}
	return string(mode)
}

func cloneLoweringEntries(in []loweringEntry) ([]loweringEntry, error) {
	out := make([]loweringEntry, len(in))
	for index := range in {
		out[index] = in[index]
		var err error
		out[index].baseline, err = cloneTableDefinition(in[index].baseline)
		if err != nil {
			return nil, err
		}
		out[index].target, err = cloneTableDefinition(in[index].target)
		if err != nil {
			return nil, err
		}
		out[index].targetColumn, err = cloneColumnPointer(in[index].targetColumn)
		if err != nil {
			return nil, err
		}
		out[index].baselineColumn, err = cloneColumnPointer(in[index].baselineColumn)
		if err != nil {
			return nil, err
		}
		out[index].targetIndex, err = cloneIndex(in[index].targetIndex)
		if err != nil {
			return nil, err
		}
		if in[index].baselineConstraint != nil {
			constraint, cloneErr := cloneTableConstraint(*in[index].baselineConstraint)
			if cloneErr != nil {
				return nil, cloneErr
			}
			out[index].baselineConstraint = &constraint
		}
		if in[index].targetConstraint != nil {
			constraint, cloneErr := cloneTableConstraint(*in[index].targetConstraint)
			if cloneErr != nil {
				return nil, cloneErr
			}
			out[index].targetConstraint = &constraint
		}
	}
	return out, nil
}

func previewOperations(entries []loweringEntry) []diff.ProposedOperation {
	out := make([]diff.ProposedOperation, len(entries))
	for index := range entries {
		out[index] = entries[index].operation
	}
	return out
}

//nolint:unused // Kept as a package-local compatibility path.
func numberSources(index int, operation *diff.ProposedOperation) {
	for offset := range operation.Forward {
		operation.Forward[offset].Source = fmt.Sprintf("%03d_%s", index, operation.Forward[offset].Source)
	}
	for offset := range operation.Reverse {
		operation.Reverse[offset].Source = fmt.Sprintf("%03d_%s", index, operation.Reverse[offset].Source)
	}
}

func renameIdentifiersFromEntry(entry loweringEntry, decision diff.RequiredDecision) (string, string, error) {
	if entry.baselineColumn == nil || entry.targetColumn == nil {
		return "", "", fmt.Errorf("postgresql schema diff: rename columns are missing")
	}
	if entry.baselineColumn.Name.Name != decision.Baseline || entry.targetColumn.Name.Name != decision.Target {
		return "", "", fmt.Errorf("postgresql schema diff: rename candidate changed")
	}
	return renderIdentifier(entry.baselineColumn.Name), renderIdentifier(entry.targetColumn.Name), nil
}

func lowerPostgreSQLEntry(entry loweringEntry, resolutions map[string]diff.Resolution) (diff.ProposedOperation, error) {
	op := entry.operation
	var forward, reverse []diff.PlannedStatement
	name := entry.operation.Summary
	switch entry.action {
	case lowerCreateTable:
		sql, err := renderCreateTable(entry.target)
		if err != nil {
			return op, err
		}
		forward = []diff.PlannedStatement{{Source: "create_table_" + filenamePart(entry.operation.Table) + ".sql", SQL: sql, ReverseSQL: fmt.Sprintf("DROP TABLE %s;\n", reverseName(entry.target.statement.Name)), Summary: name}}
	case lowerCreateIndex:
		copy := *entry.targetIndex
		copy.IfNotExists = false
		sql, err := serialize(&copy)
		if err != nil {
			return op, err
		}
		forward = []diff.PlannedStatement{{Source: "create_index_" + filenamePart(entry.operation.Constraint) + ".sql", SQL: sql, ReverseSQL: fmt.Sprintf("DROP INDEX %s;\n", reverseName(*copy.Name)), Summary: name}}
	case lowerAlterNullability:
		if entry.baselineColumn == nil || entry.targetColumn == nil {
			return op, fmt.Errorf("postgresql schema diff: nullability columns are missing")
		}
		verb := "DROP NOT NULL"
		reverseVerb := "SET NOT NULL"
		if !columnNullable(*entry.targetColumn) {
			verb, reverseVerb = reverseVerb, verb
		}
		forward = []diff.PlannedStatement{{Source: "alter_nullability_" + filenamePart(entry.operation.Table) + "_" + filenamePart(entry.operation.Column) + ".sql", SQL: fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s %s;\n", reverseName(entry.target.statement.Name), reverseIdentifier(entry.targetColumn.Name), verb), ReverseSQL: fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s %s;\n", reverseName(entry.target.statement.Name), reverseIdentifier(entry.targetColumn.Name), reverseVerb), Summary: name}}
	case lowerReplaceConstraint:
		if entry.baselineConstraint == nil || entry.targetConstraint == nil {
			return op, fmt.Errorf("postgresql schema diff: replacement constraint is missing")
		}
		targetFragment, err := renderTableConstraint(entry.target, *entry.targetConstraint)
		if err != nil {
			return op, err
		}
		baselineFragment, err := renderTableConstraint(entry.baseline, *entry.baselineConstraint)
		if err != nil {
			return op, err
		}
		tableName := reverseName(entry.target.statement.Name)
		constraintName := reverseIdentifier(*entry.targetConstraint.Name)
		forward = []diff.PlannedStatement{
			{Source: "drop_constraint_" + filenamePart(entry.operation.Table) + "_" + filenamePart(entry.operation.Constraint) + ".sql", SQL: fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;\n", tableName, constraintName), Summary: name},
			{Source: "add_constraint_" + filenamePart(entry.operation.Table) + "_" + filenamePart(entry.operation.Constraint) + ".sql", SQL: fmt.Sprintf("ALTER TABLE %s ADD %s;\n", tableName, targetFragment), Summary: name},
		}
		reverseDrop := fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;\n", reverseName(entry.baseline.statement.Name), reverseIdentifier(*entry.baselineConstraint.Name))
		reverseAdd := fmt.Sprintf("ALTER TABLE %s ADD %s;\n", reverseName(entry.baseline.statement.Name), baselineFragment)
		forward[0].ReverseSQL = reverseAdd
		forward[1].ReverseSQL = reverseDrop
		reverse = []diff.PlannedStatement{
			{Source: forward[0].Source, SQL: reverseDrop, ReverseSQL: reverseAdd, Summary: name},
			{Source: forward[1].Source, SQL: reverseAdd, ReverseSQL: reverseDrop, Summary: name},
		}
	case lowerRenameColumn:
		from, to, err := renameIdentifiersFromEntry(entry, diff.RequiredDecision{Baseline: entry.baselineColumn.Name.Name, Target: entry.targetColumn.Name.Name})
		if err != nil {
			return op, err
		}
		forward = []diff.PlannedStatement{{Source: "rename_column_" + filenamePart(entry.operation.Table) + ".sql", SQL: fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s;\n", reverseName(entry.target.statement.Name), from, to), ReverseSQL: fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s;\n", reverseName(entry.target.statement.Name), to, from), Summary: name}}
	case lowerBackfill:
		column := *entry.targetColumn
		staged := column
		staged.Constraints = nil
		for _, c := range column.Constraints {
			if c.Kind != pgquery.ConstraintNotNull {
				staged.Constraints = append(staged.Constraints, c)
			}
		}
		add, err := addColumnSQL(entry.target, staged)
		if err != nil {
			return op, err
		}
		resolution := resolutions[entry.decisionID]
		forward = []diff.PlannedStatement{{Source: "add_column_" + filenamePart(entry.operation.Table) + "_" + filenamePart(entry.operation.Column) + ".sql", SQL: add + strings.TrimSpace(resolution.BackfillSQL) + fmt.Sprintf("\nALTER TABLE %s ALTER COLUMN %s SET NOT NULL;\n", reverseName(entry.target.statement.Name), reverseIdentifier(column.Name)), ReverseSQL: fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;\n", reverseName(entry.target.statement.Name), reverseIdentifier(column.Name)), Summary: name}}
	default:
		if entry.targetColumn == nil {
			return op, fmt.Errorf("postgresql schema diff: missing column for %s", name)
		}
		add, err := addColumnSQL(entry.target, *entry.targetColumn)
		if err != nil {
			return op, err
		}
		forward = []diff.PlannedStatement{{Source: "add_column_" + filenamePart(entry.operation.Table) + "_" + filenamePart(entry.operation.Column) + ".sql", SQL: add, ReverseSQL: fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;\n", reverseName(entry.target.statement.Name), reverseIdentifier(entry.targetColumn.Name)), Summary: name}}
	}
	for index := range forward {
		forward[index].Source = strings.TrimPrefix(forward[index].Source, "000_")
	}
	if entry.action != lowerReplaceConstraint {
		reverse = []diff.PlannedStatement{{Source: forward[0].Source, SQL: forward[0].ReverseSQL, ReverseSQL: forward[0].SQL, Summary: forward[0].Summary}}
	}
	for index := range reverse {
		reverse[index].Source = strings.TrimPrefix(reverse[index].Source, "000_")
	}
	op.Forward, op.Reverse = forward, reverse
	return op, nil
}

type schemaSnapshot struct {
	tables  map[string]tableDefinition
	indexes map[string]indexDefinition
}

func attachFacts(source string, statement *pgquery.CreateTableStatement, tableKey string, scanned tableFacts) (map[string]identityMode, map[foreignKeyKey]foreignKeyActions, error) {
	identities := make(map[string]identityMode, len(scanned.identities))
	columns := make(map[string]int, len(statement.Columns))
	for _, column := range statement.Columns {
		columns[identifierKey(column.Name)]++
	}
	for column, mode := range scanned.identities {
		if columns[column] != 1 {
			return nil, nil, fmt.Errorf("postgresql schema source %q table %s identity column %s does not attach exactly once", source, tableKey, column)
		}
		identities[column] = mode
	}
	foreignKeys := make(map[foreignKeyKey]foreignKeyActions, len(scanned.foreignKeys))
	parsed := make(map[foreignKeyKey]int)
	for _, constraint := range statement.Constraints {
		if constraint.Kind == pgquery.ConstraintForeignKey && constraint.References != nil && constraint.Name != nil {
			parsed[foreignKeyKey{tableKey, identifierKey(*constraint.Name), false}]++
		}
	}
	for _, column := range statement.Columns {
		for _, constraint := range column.Constraints {
			if constraint.Kind == pgquery.ConstraintReferences && constraint.References != nil {
				parsed[foreignKeyKey{tableKey, identifierKey(column.Name), true}]++
			}
		}
	}
	for key, actions := range scanned.foreignKeys {
		kind := "table"
		if key.inline {
			kind = "inline"
		}
		if parsed[key] != 1 {
			return nil, nil, fmt.Errorf("postgresql schema source %q table %s %s foreign key %s does not attach exactly once", source, tableKey, kind, key.constraint)
		}
		foreignKeys[key] = actions
	}
	for key := range parsed {
		if _, ok := scanned.foreignKeys[key]; !ok {
			return nil, nil, fmt.Errorf("postgresql schema source %q table %s foreign key %s has no scanned action facts", source, tableKey, key.constraint)
		}
	}
	return identities, foreignKeys, nil
}

// Dialect identifies PostgreSQL snapshots.
func (*schemaSnapshot) Dialect() string {
	return "postgresql"
}

type tableDefinition struct {
	source      string
	statement   *pgquery.CreateTableStatement
	identities  map[string]identityMode
	foreignKeys map[foreignKeyKey]foreignKeyActions
}

func (s *schemaSnapshot) addTable(source string, statement *pgquery.CreateTableStatement, identities map[string]identityMode, foreignKeys map[foreignKeyKey]foreignKeyActions) error {
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
	cloned, err := cloneTableDefinition(tableDefinition{source: source, statement: statement, identities: identities, foreignKeys: foreignKeys})
	if err != nil {
		return err
	}
	s.tables[key] = cloned
	return nil
}

func cloneIdentityModes(in map[string]identityMode) map[string]identityMode {
	if in == nil {
		return nil
	}
	out := make(map[string]identityMode, len(in))
	for key, mode := range in {
		out[key] = mode
	}
	return out
}
func cloneForeignKeyActions(in map[foreignKeyKey]foreignKeyActions) map[foreignKeyKey]foreignKeyActions {
	if in == nil {
		return nil
	}
	out := make(map[foreignKeyKey]foreignKeyActions, len(in))
	for key, actions := range in {
		out[key] = actions
	}
	return out
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
	cloned, err := cloneIndex(statement)
	if err != nil {
		return err
	}
	s.indexes[key] = indexDefinition{source: source, statement: cloned}
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

type loweringAction uint8

const (
	lowerCreateTable loweringAction = iota
	lowerAddColumn
	lowerCreateIndex
	lowerRenameColumn
	lowerBackfill
	lowerAlterNullability
	lowerReplaceConstraint
)

type loweringEntry struct {
	operation          diff.ProposedOperation
	action             loweringAction
	baseline           tableDefinition
	target             tableDefinition
	targetColumn       *pgquery.ColumnDefinition
	baselineColumn     *pgquery.ColumnDefinition
	targetIndex        *pgquery.CreateIndexStatement
	baselineConstraint *pgquery.TableConstraint
	targetConstraint   *pgquery.TableConstraint
	decisionID         string
}

type statementRef struct {
	operation int
	statement int
}

func scheduleLoweredOperations(operations []diff.ProposedOperation, actions []loweringAction) []diff.PlannedStatement {
	forwardRefs := make([]statementRef, 0)
	for index := range operations {
		if actions[index] == lowerReplaceConstraint {
			forwardRefs = append(forwardRefs, statementRef{operation: index, statement: 0})
		}
	}
	for index := range operations {
		if actions[index] != lowerReplaceConstraint {
			forwardRefs = append(forwardRefs, statementRef{operation: index, statement: 0})
		}
	}
	for index := range operations {
		if actions[index] == lowerReplaceConstraint {
			forwardRefs = append(forwardRefs, statementRef{operation: index, statement: 1})
		}
	}
	statements := make([]diff.PlannedStatement, 0, len(forwardRefs))
	sources := make(map[statementRef]string, len(forwardRefs))
	for ordinal, ref := range forwardRefs {
		statement := &operations[ref.operation].Forward[ref.statement]
		statement.Source = fmt.Sprintf("%03d_%s", ordinal+1, statement.Source)
		sources[ref] = statement.Source
		statements = append(statements, *statement)
	}
	for index := range operations {
		for statementIndex := range operations[index].Reverse {
			forwardIndex := statementIndex
			if actions[index] == lowerReplaceConstraint {
				forwardIndex = len(operations[index].Forward) - statementIndex - 1
			}
			operations[index].Reverse[statementIndex].Source = sources[statementRef{operation: index, statement: forwardIndex}]
		}
	}
	return statements
}

func createTableEntry(table tableDefinition) (loweringEntry, error) {
	copy, err := cloneTableDefinition(table)
	if err != nil {
		return loweringEntry{}, err
	}
	name := displayName(copy.statement.Name)
	return loweringEntry{operation: diff.ProposedOperation{ID: diff.OperationID(diff.OperationCreateTable, "postgresql", name, "", ""), Table: name, Summary: "create table " + name, Kind: diff.OperationCreateTable}, action: lowerCreateTable, target: copy}, nil
}

func createIndexEntry(index indexDefinition) (loweringEntry, error) {
	name := displayName(*index.statement.Name)
	indexCopy, err := cloneIndex(index.statement)
	if err != nil {
		return loweringEntry{}, err
	}
	return loweringEntry{operation: diff.ProposedOperation{ID: diff.OperationID(diff.OperationReplaceConstraint, "postgresql", displayName(indexCopy.Table), "", name), Table: displayName(indexCopy.Table), Constraint: name, Summary: "create index " + name, Kind: diff.OperationReplaceConstraint}, action: lowerCreateIndex, targetIndex: indexCopy}, nil
}

func diffTable(baseline, target tableDefinition) ([]loweringEntry, []string, []diff.RequiredDecision, error) {
	entries := make([]loweringEntry, 0)
	constraintEntries := make([]loweringEntry, 0)
	diagnostics := make([]string, 0)
	decisions := make([]diff.RequiredDecision, 0)
	normalizedBaseline := normalizedTable(baseline.statement, false)
	normalizedTarget := normalizedTable(target.statement, false)
	for index, column := range baseline.statement.Columns {
		if index >= len(target.statement.Columns) || column.Name.Name != target.statement.Columns[index].Name.Name {
			continue
		}
		if column.Name.Quoted != target.statement.Columns[index].Name.Quoted && strings.ToLower(column.Name.Name) != column.Name.Name {
			diagnostics = append(diagnostics, fmt.Sprintf("column %s.%s was removed", displayName(baseline.statement.Name), column.Name.Name))
		}
	}
	if baseline.statement.Persistence != target.statement.Persistence {
		diagnostics = append(diagnostics, fmt.Sprintf("table %s persistence changed", displayName(target.statement.Name)))
	}
	constraintsChanged := !ast.Equal(normalizedBaseline.Constraints, normalizedTarget.Constraints) || !equalForeignKeyActions(baseline.foreignKeys, target.foreignKeys)
	baseNamed, baseNamedErr := namedTableConstraints(baseline)
	if baseNamedErr != nil {
		return nil, nil, nil, baseNamedErr
	}
	targetNamed, targetNamedErr := namedTableConstraints(target)
	if targetNamedErr != nil {
		return nil, nil, nil, targetNamedErr
	}
	constraintReplacements := 0
	unhandledConstraintChange := !ast.Equal(anonymousConstraints(normalizedBaseline.Constraints), anonymousConstraints(normalizedTarget.Constraints))
	baseKeys := make([]string, 0, len(baseNamed))
	for key := range baseNamed {
		baseKeys = append(baseKeys, key)
	}
	sort.Strings(baseKeys)
	for _, key := range baseKeys {
		left := baseNamed[key]
		right, exists := targetNamed[key]
		if !exists {
			unhandledConstraintChange = true
			continue
		}
		if sameNamedConstraint(baseline, left, target, right) {
			continue
		}
		if left.Kind != right.Kind || (left.Kind == pgquery.ConstraintForeignKey && (left.References == nil || right.References == nil)) {
			unhandledConstraintChange = true
			continue
		}
		if left.Name == nil || right.Name == nil {
			continue
		}
		name := displayName(target.statement.Name)
		constraintName := displayName(pgquery.QualifiedName{*right.Name})
		leftCopy, cloneErr := cloneTableConstraint(left)
		if cloneErr != nil {
			return nil, nil, nil, cloneErr
		}
		rightCopy, cloneErr := cloneTableConstraint(right)
		if cloneErr != nil {
			return nil, nil, nil, cloneErr
		}
		constraintEntries = append(constraintEntries, loweringEntry{
			operation: diff.ProposedOperation{ID: diff.OperationID(diff.OperationReplaceConstraint, "postgresql", name, "", constraintName), Table: name, Constraint: constraintName, Summary: "replace constraint " + name + "." + constraintName, Kind: diff.OperationReplaceConstraint},
			action:    lowerReplaceConstraint, baseline: baseline, target: target, baselineConstraint: &leftCopy, targetConstraint: &rightCopy,
		})
		constraintReplacements++
	}
	for key := range targetNamed {
		if _, exists := baseNamed[key]; !exists {
			unhandledConstraintChange = true
		}
	}
	if constraintsChanged && (unhandledConstraintChange || constraintReplacements == 0 && len(baseNamed) == len(targetNamed)) {
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
	removed, added := make([]pgquery.ColumnDefinition, 0, 1), make([]pgquery.ColumnDefinition, 0, 1)
	for _, column := range normalizedBaseline.Columns {
		if _, exists := targetColumns[column.Name.Name]; !exists {
			removed = append(removed, column)
		}
	}
	for _, column := range normalizedTarget.Columns {
		if _, exists := baselineColumns[column.Name.Name]; !exists {
			added = append(added, column)
		}
	}
	rename := len(removed) == 1 && len(added) == 1 && removed[0].Name.Name != added[0].Name.Name && removed[0].Name.Quoted == added[0].Name.Quoted
	if rename {
		left, right := removed[0], added[0]
		left.Name.Name, right.Name.Name = "", ""
		rename = ast.Equal(left, right)
		if rename {
			decisions = append(decisions, diff.RequiredDecision{ID: diff.DecisionID(diff.DecisionRename, "postgresql", displayName(target.statement.Name), added[0].Name.Name), Kind: diff.DecisionRename, Table: displayName(target.statement.Name), Column: added[0].Name.Name, Baseline: removed[0].Name.Name, Target: added[0].Name.Name, Reason: "column rename requires caller confirmation"})
			name := displayName(target.statement.Name)
			decisionID := diff.DecisionID(diff.DecisionRename, "postgresql", name, added[0].Name.Name)
			clonedTarget, err := cloneTableDefinition(target)
			if err != nil {
				return nil, nil, nil, err
			}
			clonedBaseline, err := cloneTableDefinition(baseline)
			if err != nil {
				return nil, nil, nil, err
			}
			clonedTargetColumn, err := cloneColumnPointer(findColumn(target.statement, added[0].Name.Name))
			if err != nil {
				return nil, nil, nil, err
			}
			clonedBaselineColumn, err := cloneColumnPointer(findColumn(baseline.statement, removed[0].Name.Name))
			if err != nil {
				return nil, nil, nil, err
			}
			entries = append(entries, loweringEntry{operation: diff.ProposedOperation{ID: diff.OperationID(diff.OperationAddColumn, "postgresql", name, added[0].Name.Name, ""), Table: name, Column: added[0].Name.Name, Summary: "rename column " + name + "." + added[0].Name.Name, Kind: diff.OperationAddColumn}, action: lowerRenameColumn, target: clonedTarget, baseline: clonedBaseline, targetColumn: clonedTargetColumn, baselineColumn: clonedBaselineColumn, decisionID: decisionID})
		}
	}
	for index, column := range target.statement.Columns {
		normalizedColumn := normalizedTarget.Columns[index]
		previous, exists := baselineColumns[normalizedColumn.Name.Name]
		if !exists {
			if rename && normalizedColumn.Name.Name == added[0].Name.Name {
				continue
			}
			if columnRequiresBackfill(column) {
				name := displayName(target.statement.Name)
				decisionID := diff.DecisionID(diff.DecisionBackfill, "postgresql", name, column.Name.Name)
				decisions = append(decisions, diff.RequiredDecision{ID: decisionID, Kind: diff.DecisionBackfill, Table: name, Column: column.Name.Name, Target: column.Name.Name, Reason: "required column needs an application-specific backfill"})
				clonedTarget, err := cloneTableDefinition(target)
				if err != nil {
					return nil, nil, nil, err
				}
				clonedColumn, err := cloneColumnPointer(&column)
				if err != nil {
					return nil, nil, nil, err
				}
				entries = append(entries, loweringEntry{operation: diff.ProposedOperation{ID: diff.OperationID(diff.OperationAddColumn, "postgresql", name, column.Name.Name, ""), Table: name, Column: column.Name.Name, Summary: "add column " + name + "." + column.Name.Name, Kind: diff.OperationAddColumn}, action: lowerBackfill, target: clonedTarget, targetColumn: clonedColumn, decisionID: decisionID})
				continue
			}
			name := displayName(target.statement.Name)
			clonedTarget, err := cloneTableDefinition(target)
			if err != nil {
				return nil, nil, nil, err
			}
			clonedColumn, err := cloneColumnPointer(&column)
			if err != nil {
				return nil, nil, nil, err
			}
			entries = append(entries, loweringEntry{operation: diff.ProposedOperation{ID: diff.OperationID(diff.OperationAddColumn, "postgresql", name, column.Name.Name, ""), Table: name, Column: column.Name.Name, Summary: "add column " + name + "." + column.Name.Name, Kind: diff.OperationAddColumn}, action: lowerAddColumn, target: clonedTarget, targetColumn: clonedColumn})
			continue
		}
		if sameColumnDefinition(baseline, previous, target, normalizedColumn) {
			continue
		}
		baselineMode, baselineIdentity := identityFor(baseline, previous.Name)
		targetMode, targetIdentity := identityFor(target, normalizedColumn.Name)
		if baselineIdentity != targetIdentity || baselineIdentity && baselineMode != targetMode {
			diagnostics = append(diagnostics, fmt.Sprintf("column %s.%s identity mode changed from %s to %s; manual migration required", displayName(target.statement.Name), column.Name.Name, identityModeName(baselineMode, baselineIdentity), identityModeName(targetMode, targetIdentity)))
			continue
		}
		if sameColumnExceptNullability(baseline, previous, target, normalizedColumn) && columnNullable(previous) != columnNullable(normalizedColumn) {
			name := displayName(target.statement.Name)
			entries = append(entries, loweringEntry{
				operation: diff.ProposedOperation{ID: diff.OperationID(diff.OperationAlterNullability, "postgresql", name, column.Name.Name, ""), Table: name, Column: column.Name.Name, Summary: "alter nullability " + name + "." + column.Name.Name, Kind: diff.OperationAlterNullability},
				action:    lowerAlterNullability, baseline: baseline, target: target,
				targetColumn: &column, baselineColumn: findColumn(baseline.statement, column.Name.Name),
			})
			continue
		}
		diagnostics = append(diagnostics, fmt.Sprintf("column %s.%s changed", displayName(target.statement.Name), column.Name.Name))
	}
	for index, column := range baseline.statement.Columns {
		normalizedColumn := normalizedBaseline.Columns[index]
		if _, exists := targetColumns[normalizedColumn.Name.Name]; !exists {
			if rename && normalizedColumn.Name.Name == removed[0].Name.Name {
				continue
			}
			diagnostics = append(diagnostics, fmt.Sprintf("column %s.%s was removed", displayName(baseline.statement.Name), column.Name.Name))
		}
	}
	entries = append(entries, constraintEntries...)
	return entries, diagnostics, decisions, nil
}

func anonymousConstraints(constraints []pgquery.TableConstraint) []pgquery.TableConstraint {
	result := make([]pgquery.TableConstraint, 0)
	for _, constraint := range constraints {
		if constraint.Name == nil {
			result = append(result, constraint)
		}
	}
	return result
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

func displayName(name pgquery.QualifiedName) string {
	return name.String()
}

func reverseName(name pgquery.QualifiedName) string {
	parts := make([]string, len(name))
	for index, part := range name {
		parts[index] = reverseIdentifier(part)
	}
	return strings.Join(parts, ".")
}

func reverseIdentifier(identifier pgquery.Identifier) string {
	if !identifier.Quoted {
		return identifier.Name
	}
	return `"` + strings.ReplaceAll(identifier.Name, `"`, `""`) + `"`
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
