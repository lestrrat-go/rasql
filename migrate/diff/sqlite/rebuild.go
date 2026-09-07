package sqlite

import (
	"fmt"
	"sort"
	"strings"

	sqlitequery "github.com/lestrrat-go/rasql-sqlite/query"
	"github.com/lestrrat-go/rasql/migrate/diff"
)

// rebuildCarrier owns one complete table rebuild.  Mappings are explicit so
// lowering never has to infer a rename from rendered SQL.
type rebuildCarrier struct {
	tableKey         string
	baseline         tableDefinition
	target           tableDefinition
	baselineIndexes  []indexDefinition
	targetIndexes    []indexDefinition
	forward          []columnMapping
	reverse          []columnMapping
	temporary        sqlitequery.QualifiedName
	temporaryReverse sqlitequery.QualifiedName
	knownFacts       bool
}

type columnMapping struct {
	destination      sqlitequery.Identifier
	source           sqlitequery.Identifier
	backfillDecision string
	sourceKind       mappingSourceKind
	expression       string
}

type mappingSourceKind uint8

const (
	mappingBaseline mappingSourceKind = iota
	mappingStagedBackfill
	mappingTargetDefault
	mappingNullableNull
)

func cloneTable(table tableDefinition) (tableDefinition, error) {
	result := table
	if table.statement != nil {
		clone, err := cloneCreateTable(table.statement)
		if err != nil {
			return tableDefinition{}, err
		}
		result.statement = clone
	}
	if table.normalized != nil {
		clone, err := cloneCreateTable(table.normalized)
		if err != nil {
			return tableDefinition{}, err
		}
		result.normalized = clone
	}
	result.foreignKeys = append([]foreignKeyActions(nil), table.foreignKeys...)
	return result, nil
}

func cloneCreateTable(statement *sqlitequery.CreateTableStatement) (*sqlitequery.CreateTableStatement, error) {
	source, err := serialize(statement)
	if err != nil {
		return nil, fmt.Errorf("serialize table carrier: %w", err)
	}
	parsed, err := sqlitequery.ParseStatement(source)
	if err != nil {
		return nil, fmt.Errorf("parse table carrier: %w", err)
	}
	clone, ok := parsed.(*sqlitequery.CreateTableStatement)
	if !ok {
		return nil, fmt.Errorf("parse table carrier: got %T", parsed)
	}
	return clone, nil
}

func chooseRebuildName(table sqlitequery.QualifiedName, occupied map[string]struct{}) (sqlitequery.QualifiedName, error) {
	if len(table) == 0 {
		return nil, fmt.Errorf("sqlite schema diff: table has no name")
	}
	base := table[len(table)-1].Name + "__rasql_rebuild"
	for i := 1; i <= 1000; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s_%d", base, i)
		}
		candidate := append(sqlitequery.QualifiedName(nil), table[:len(table)-1]...)
		candidate = append(candidate, sqlitequery.Identifier{Name: name})
		if _, exists := occupied[qualifiedNameKey(candidate)]; !exists {
			if _, exists := occupied[sqliteIdentifierKey(name)]; exists {
				continue
			}
			return candidate, nil
		}
	}
	return nil, fmt.Errorf("sqlite schema diff: table %s has no available rebuild temporary name", displayName(table))
}

func buildRebuildCarrier(baseline, target tableDefinition, baselineIndexes, targetIndexes []indexDefinition, decisions []diff.RequiredDecision, facts LiveCatalogFacts, known bool) (rebuildCarrier, error) {
	if baseline.statement == nil || target.statement == nil || baseline.normalized == nil || target.normalized == nil {
		return rebuildCarrier{}, fmt.Errorf("sqlite schema diff: clone rebuild carrier: table AST is nil")
	}
	for _, table := range []tableDefinition{baseline, target} {
		for _, column := range table.statement.Columns {
			for _, constraint := range column.Constraints {
				if constraint.Kind == sqlitequery.ConstraintGenerated {
					return rebuildCarrier{}, fmt.Errorf("sqlite schema diff: rebuild table %s cannot represent generated column %q", displayName(table.statement.Name), column.Name.Name)
				}
			}
		}
	}
	occupied := map[string]struct{}{qualifiedNameKey(baseline.statement.Name): {}, qualifiedNameKey(target.statement.Name): {}}
	for _, name := range facts.ObjectNames {
		occupied[sqliteIdentifierKey(name)] = struct{}{}
		occupied[qualifiedNameKey(sqlitequery.QualifiedName{{Name: name}})] = struct{}{}
	}
	for _, index := range targetIndexes {
		occupied[qualifiedNameKey(index.statement.Name)] = struct{}{}
	}
	temporary, err := chooseRebuildName(target.statement.Name, occupied)
	if err != nil {
		return rebuildCarrier{}, err
	}
	occupied[qualifiedNameKey(temporary)] = struct{}{}
	temporaryReverse, err := chooseRebuildName(baseline.statement.Name, occupied)
	if err != nil {
		return rebuildCarrier{}, err
	}
	decisionByColumn := make(map[string]string)
	renameByTarget := make(map[string]string)
	renameByBaseline := make(map[string]string)
	seenRenameBaseline, seenRenameTarget := make(map[string]struct{}), make(map[string]struct{})
	baseColumns := make(map[string]sqlitequery.Identifier)
	for _, column := range baseline.normalized.Columns {
		baseColumns[sqliteIdentifierKey(column.Name.Name)] = column.Name
	}
	targetNames := make(map[string]struct{})
	for _, column := range target.normalized.Columns {
		targetNames[sqliteIdentifierKey(column.Name.Name)] = struct{}{}
	}
	for _, decision := range decisions {
		if decision.Kind == diff.DecisionBackfill {
			decisionByColumn[sqliteIdentifierKey(decision.Column)] = decision.ID
		}
		if decision.Kind == diff.DecisionRename {
			baseKey, targetKey := sqliteIdentifierKey(decision.Baseline), sqliteIdentifierKey(decision.Target)
			if _, ok := baseColumns[baseKey]; !ok {
				return rebuildCarrier{}, fmt.Errorf("rename %s.%s has unknown baseline", displayName(target.statement.Name), decision.Baseline)
			}
			if _, ok := targetNames[targetKey]; !ok {
				return rebuildCarrier{}, fmt.Errorf("rename %s.%s has unknown target", displayName(target.statement.Name), decision.Target)
			}
			if _, ok := seenRenameBaseline[baseKey]; ok {
				return rebuildCarrier{}, fmt.Errorf("duplicate rename baseline %s.%s", displayName(target.statement.Name), decision.Baseline)
			}
			if _, ok := seenRenameTarget[targetKey]; ok {
				return rebuildCarrier{}, fmt.Errorf("duplicate rename target %s.%s", displayName(target.statement.Name), decision.Target)
			}
			seenRenameBaseline[baseKey], seenRenameTarget[targetKey] = struct{}{}, struct{}{}
			renameByTarget[targetKey] = decision.Baseline
			renameByBaseline[baseKey] = decision.Target
		}
	}
	forward := make([]columnMapping, 0, len(target.normalized.Columns))
	reverse := make([]columnMapping, 0, len(baseline.normalized.Columns))
	for _, column := range target.normalized.Columns {
		key := sqliteIdentifierKey(column.Name.Name)
		source, ok := baseColumns[key]
		if renamed, exists := renameByTarget[key]; exists {
			source = sqlitequery.Identifier{Name: renamed}
			ok = true
		}
		if !ok {
			mapping := columnMapping{destination: column.Name, source: column.Name, backfillDecision: decisionByColumn[key]}
			if mapping.backfillDecision != "" {
				mapping.sourceKind = mappingStagedBackfill
			} else if expression, present, defaultErr := columnDefaultSQL(column); defaultErr != nil {
				return rebuildCarrier{}, fmt.Errorf("render default %s.%s: %w", displayName(target.statement.Name), column.Name.Name, defaultErr)
			} else if present {
				if expression == "" {
					return rebuildCarrier{}, fmt.Errorf("render default %s.%s: empty expression", displayName(target.statement.Name), column.Name.Name)
				}
				mapping.sourceKind, mapping.expression = mappingTargetDefault, expression
			} else {
				mapping.sourceKind = mappingNullableNull
			}
			forward = append(forward, mapping)
			continue
		}
		forward = append(forward, columnMapping{destination: column.Name, source: source, sourceKind: mappingBaseline})
	}
	targetColumns := make(map[string]sqlitequery.Identifier)
	for _, column := range target.normalized.Columns {
		targetColumns[sqliteIdentifierKey(column.Name.Name)] = column.Name
	}
	for _, column := range baseline.normalized.Columns {
		destination := column.Name
		source := column.Name
		if renamed, exists := renameByBaseline[sqliteIdentifierKey(source.Name)]; exists {
			source = sqlitequery.Identifier{Name: renamed}
		}
		if targetColumnName, ok := targetColumns[sqliteIdentifierKey(source.Name)]; ok {
			reverse = append(reverse, columnMapping{destination: destination, source: targetColumnName})
		}
	}
	if err := validateColumnMappings(target.statement.Name, baseline.normalized.Columns, target.normalized.Columns, forward, reverse); err != nil {
		return rebuildCarrier{}, err
	}
	sort.Slice(forward, func(i, j int) bool {
		return sqliteIdentifierKey(forward[i].destination.Name) < sqliteIdentifierKey(forward[j].destination.Name)
	})
	sort.Slice(reverse, func(i, j int) bool {
		return sqliteIdentifierKey(reverse[i].destination.Name) < sqliteIdentifierKey(reverse[j].destination.Name)
	})
	baseClone, err := cloneTable(baseline)
	if err != nil {
		return rebuildCarrier{}, fmt.Errorf("sqlite schema diff: clone baseline carrier: %w", err)
	}
	targetClone, err := cloneTable(target)
	if err != nil {
		return rebuildCarrier{}, fmt.Errorf("sqlite schema diff: clone target carrier: %w", err)
	}
	baseIndexes, err := cloneIndexes(baselineIndexes)
	if err != nil {
		return rebuildCarrier{}, fmt.Errorf("sqlite schema diff: clone baseline indexes: %w", err)
	}
	targetIndexesClone, err := cloneIndexes(targetIndexes)
	if err != nil {
		return rebuildCarrier{}, fmt.Errorf("sqlite schema diff: clone target indexes: %w", err)
	}
	return rebuildCarrier{tableKey: qualifiedNameKey(target.statement.Name), baseline: baseClone, target: targetClone, baselineIndexes: baseIndexes, targetIndexes: targetIndexesClone, forward: append([]columnMapping(nil), forward...), reverse: append([]columnMapping(nil), reverse...), temporary: temporary, temporaryReverse: temporaryReverse, knownFacts: known}, nil
}

func cloneIndexes(indexes []indexDefinition) ([]indexDefinition, error) {
	result := make([]indexDefinition, len(indexes))
	for i, index := range indexes {
		result[i] = index
		if index.statement == nil {
			continue
		}
		source, err := serialize(index.statement)
		if err != nil {
			return nil, fmt.Errorf("serialize index carrier: %w", err)
		}
		parsed, err := sqlitequery.ParseStatement(source)
		if err != nil {
			return nil, fmt.Errorf("parse index carrier: %w", err)
		}
		clone, ok := parsed.(*sqlitequery.CreateIndexStatement)
		if !ok {
			return nil, fmt.Errorf("parse index carrier: got %T", parsed)
		}
		result[i].statement = clone
	}
	return result, nil
}

func renderCreateTableAs(table tableDefinition, name sqlitequery.QualifiedName) (string, error) {
	copy, err := cloneTable(table)
	if err != nil {
		return "", err
	}
	copy.statement.Name = name
	copy.statement.IfNotExists = false
	serialized, err := serializeWithReferenceActions(copy.statement, copy.foreignKeys)
	if err != nil {
		return "", err
	}
	return restoreDefaultExpressions(serialized, copy.statement.Columns)
}

func restoreDefaultExpressions(source string, columns []sqlitequery.ColumnDefinition) (string, error) {
	tokens := tokenizeSQLite(source)
	type replacement struct {
		start, end int
		value      string
	}
	replacements := make([]replacement, 0)
	defaultIndex := 0
	for i, token := range tokens {
		if !token.word || !strings.EqualFold(token.text, "DEFAULT") {
			continue
		}
		for defaultIndex < len(columns) && !hasColumnConstraint(columns[defaultIndex], sqlitequery.ConstraintDefault) {
			defaultIndex++
		}
		if defaultIndex >= len(columns) {
			break
		}
		column := columns[defaultIndex]
		defaultIndex++
		var expression sqlitequery.Expression
		for _, constraint := range column.Constraints {
			if constraint.Kind == sqlitequery.ConstraintDefault {
				expression = constraint.Expression
				break
			}
		}
		binary, ok := expression.(*sqlitequery.BinaryExpression)
		if !ok {
			continue
		}
		start, end, err := defaultExpressionRange(tokens, i)
		if err != nil {
			return "", err
		}
		rendered, err := renderDefaultExpression(binary)
		if err != nil {
			return "", err
		}
		replacements = append(replacements, replacement{start: start, end: end, value: rendered})
	}
	for i := len(replacements) - 1; i >= 0; i-- {
		replacement := replacements[i]
		source = source[:replacement.start] + replacement.value + source[replacement.end:]
	}
	return source, nil
}

func explicitCopy(destination, source sqlitequery.QualifiedName, mappings []columnMapping, _ tableDefinition) string {
	dest := make([]string, len(mappings))
	src := make([]string, len(mappings))
	for i, mapping := range mappings {
		dest[i] = reverseIdentifier(mapping.destination)
		switch mapping.sourceKind {
		case mappingBaseline, mappingStagedBackfill:
			src[i] = reverseIdentifier(mapping.source)
		case mappingTargetDefault:
			src[i] = mapping.expression
		default:
			src[i] = "NULL"
		}
	}
	return fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s;\n", reverseName(destination), strings.Join(dest, ", "), strings.Join(src, ", "), reverseName(source))
}

func lowerSQLiteRebuild(carrier rebuildCarrier, resolutions map[string]diff.Resolution) (diff.LoweringResult, error) {
	cloned, err := cloneRebuildCarrier(carrier)
	if err != nil {
		return diff.LoweringResult{}, fmt.Errorf("sqlite schema diff: clone rebuild carrier: %w", err)
	}
	carrier = cloned
	if !carrier.knownFacts {
		return diff.LoweringResult{}, fmt.Errorf("sqlite schema diff: rebuild table %q requires live trigger and view dependency inspection", displayName(carrier.target.statement.Name))
	}
	table := displayName(carrier.target.statement.Name)
	forward := make([]diff.PlannedStatement, 0, len(carrier.forward)+6+len(carrier.targetIndexes))
	for _, mapping := range carrier.forward {
		if mapping.backfillDecision == "" {
			continue
		}
		resolution, ok := resolutions[mapping.backfillDecision]
		if !ok {
			return diff.LoweringResult{}, fmt.Errorf("sqlite schema diff: missing backfill resolution %q", mapping.backfillDecision)
		}
		if err := validateSQLiteBackfill(resolution.BackfillSQL, carrier.target.statement.Name, mapping.destination); err != nil {
			return diff.LoweringResult{}, fmt.Errorf("sqlite schema diff: backfill resolution %q: %w", mapping.backfillDecision, err)
		}
		stage := targetColumn(carrier.target, mapping.destination.Name)
		stage.Constraints = stagedConstraints(stage.Constraints)
		statement := &sqlitequery.AlterTableStatement{Name: carrier.target.statement.Name, Action: sqlitequery.AlterTableAction{Kind: sqlitequery.AlterTableAddColumn, Column: &stage}}
		sql, err := serialize(statement)
		if err != nil {
			return diff.LoweringResult{}, err
		}
		forward = append(forward, diff.PlannedStatement{Source: fmt.Sprintf("00_stage_%s.sql", filenamePart(mapping.destination.Name)), SQL: sql, ReverseSQL: "-- staging column is removed by the structural rebuild\n", Summary: "stage " + table + "." + mapping.destination.Name})
		forward = append(forward, diff.PlannedStatement{Source: fmt.Sprintf("01_backfill_%s.sql", filenamePart(mapping.destination.Name)), SQL: strings.TrimSpace(resolution.BackfillSQL) + "\n", ReverseSQL: "-- backfill is removed by the structural rebuild\n", Summary: "backfill " + table + "." + mapping.destination.Name})
	}
	created, err := renderCreateTableAs(carrier.target, carrier.temporary)
	if err != nil {
		return diff.LoweringResult{}, err
	}
	forward = append(forward,
		diff.PlannedStatement{Source: "02_create_rebuild.sql", SQL: created + "\n", Summary: "create rebuild table " + table},
		diff.PlannedStatement{Source: "03_copy_rebuild.sql", SQL: explicitCopy(carrier.temporary, carrier.target.statement.Name, carrier.forward, carrier.baseline), Summary: "copy " + table},
		diff.PlannedStatement{Source: "04_drop_table.sql", SQL: fmt.Sprintf("DROP TABLE %s;\n", reverseName(carrier.target.statement.Name)), Summary: "drop " + table},
		diff.PlannedStatement{Source: "05_rename_rebuild.sql", SQL: fmt.Sprintf("ALTER TABLE %s RENAME TO %s;\n", reverseName(carrier.temporary), reverseIdentifier(carrier.target.statement.Name[len(carrier.target.statement.Name)-1])), Summary: "rename rebuild " + table},
	)
	for _, index := range carrier.targetIndexes {
		copy := *index.statement
		copy.IfNotExists = false
		sql, err := serialize(&copy)
		if err != nil {
			return diff.LoweringResult{}, err
		}
		forward = append(forward, diff.PlannedStatement{Source: "06_index_" + filenamePart(displayName(copy.Name)) + ".sql", SQL: sql, ReverseSQL: fmt.Sprintf("DROP INDEX %s;\n", reverseName(copy.Name)), Summary: "create index " + displayName(copy.Name)})
	}
	// Down files are consumed in descending source order. Assign each source's
	// inverse so that this order creates the baseline table before copying it.
	baseCreated, err := renderCreateTableAs(carrier.baseline, carrier.temporaryReverse)
	if err != nil {
		return diff.LoweringResult{}, err
	}
	reverse := []string{baseCreated + "\n", explicitCopy(carrier.temporaryReverse, carrier.target.statement.Name, carrier.reverse, carrier.target), fmt.Sprintf("DROP TABLE %s;\n", reverseName(carrier.target.statement.Name)), fmt.Sprintf("ALTER TABLE %s RENAME TO %s;\n", reverseName(carrier.temporaryReverse), reverseIdentifier(carrier.baseline.statement.Name[len(carrier.baseline.statement.Name)-1]))}
	structuralEnd := len(forward) - 1 - len(carrier.targetIndexes)
	for i := range reverse {
		index := structuralEnd - i
		if index >= 0 {
			forward[index].ReverseSQL = reverse[i]
		}
	}
	for _, index := range carrier.baselineIndexes {
		copy := *index.statement
		copy.IfNotExists = false
		sql, err := serialize(&copy)
		if err != nil {
			return diff.LoweringResult{}, err
		}
		forward[0].ReverseSQL += sql
	}
	forwardCopy := append([]diff.PlannedStatement(nil), forward...)
	reverseOperations := make([]diff.PlannedStatement, len(forward))
	for index := range forward {
		artifact := forward[len(forward)-1-index]
		reverseOperations[index] = diff.PlannedStatement{
			Source:     artifact.Source,
			SQL:        artifact.ReverseSQL,
			ReverseSQL: artifact.SQL,
			Summary:    "reverse " + artifact.Summary,
		}
	}
	operation := diff.ProposedOperation{ID: diff.OperationID(diff.OperationRebuildTable, "sqlite", table, "", ""), Table: table, Summary: "rebuild table " + table, Kind: diff.OperationRebuildTable, Forward: forwardCopy, Reverse: reverseOperations}
	return diff.LoweringResult{Operations: []diff.ProposedOperation{operation}, Statements: forward}, nil
}

func stagedConstraints(constraints []sqlitequery.ColumnConstraint) []sqlitequery.ColumnConstraint {
	result := make([]sqlitequery.ColumnConstraint, 0, len(constraints))
	for _, constraint := range constraints {
		switch constraint.Kind {
		case sqlitequery.ConstraintNotNull, sqlitequery.ConstraintPrimaryKey, sqlitequery.ConstraintUnique,
			sqlitequery.ConstraintGenerated, sqlitequery.ConstraintReferences:
			continue
		default:
			result = append(result, constraint)
		}
	}
	return result
}

func columnDefaultSQL(column sqlitequery.ColumnDefinition) (string, bool, error) {
	count := 0
	for _, constraint := range column.Constraints {
		if constraint.Kind == sqlitequery.ConstraintDefault {
			count++
		}
	}
	if count == 0 {
		return "", false, nil
	}
	if count != 1 {
		return "", true, fmt.Errorf("duplicate DEFAULT constraints")
	}
	for _, constraint := range column.Constraints {
		if expression, ok := constraint.Expression.(*sqlitequery.BinaryExpression); ok && constraint.Kind == sqlitequery.ConstraintDefault {
			rendered, err := renderDefaultExpression(expression)
			if err != nil {
				return "", true, err
			}
			return rendered, true, nil
		}
	}
	statement := &sqlitequery.CreateTableStatement{Name: sqlitequery.QualifiedName{{Name: "carrier"}}, Columns: []sqlitequery.ColumnDefinition{column}}
	source, err := serialize(statement)
	if err != nil {
		return "", true, err
	}
	expression, err := extractDefaultExpression(source)
	if err != nil {
		return "", true, err
	}
	return expression, true, nil
}

func renderDefaultExpression(expression sqlitequery.Expression) (string, error) {
	if binary, ok := expression.(*sqlitequery.BinaryExpression); ok {
		left, err := renderDefaultExpression(binary.Left)
		if err != nil {
			return "", err
		}
		right, err := renderDefaultExpression(binary.Right)
		if err != nil {
			return "", err
		}
		return "(" + left + " " + binary.Operator + " " + right + ")", nil
	}
	statement := &sqlitequery.CreateTableStatement{Name: sqlitequery.QualifiedName{{Name: "carrier"}}, Columns: []sqlitequery.ColumnDefinition{{Name: sqlitequery.Identifier{Name: "value"}, Constraints: []sqlitequery.ColumnConstraint{{Kind: sqlitequery.ConstraintDefault, Expression: expression}}}}}
	source, err := serialize(statement)
	if err != nil {
		return "", err
	}
	return extractDefaultExpression(source)
}

func extractDefaultExpression(source string) (string, error) {
	tokens := tokenizeSQLite(source)
	for i, token := range tokens {
		if !token.word || !strings.EqualFold(token.text, "DEFAULT") || i+1 >= len(tokens) {
			continue
		}
		start, end, err := defaultExpressionRange(tokens, i)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(source[start:end]), nil
	}
	return "", fmt.Errorf("DEFAULT token was not found")
}

func defaultExpressionRange(tokens []sqliteToken, defaultIndex int) (int, int, error) {
	if defaultIndex+1 >= len(tokens) {
		return 0, 0, fmt.Errorf("DEFAULT expression has no delimiter")
	}
	start, depth, end := tokens[defaultIndex+1].start, 0, -1
	for j := defaultIndex + 1; j < len(tokens); j++ {
		switch tokens[j].text {
		case "(":
			depth++
		case ")":
			if depth == 0 {
				end = tokens[j].start
			} else {
				depth--
			}
		case ",":
			if depth == 0 {
				end = tokens[j].start
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 || depth < 0 {
		return 0, 0, fmt.Errorf("DEFAULT expression has no delimiter")
	}
	return start, end, nil
}

func validateColumnMappings(table sqlitequery.QualifiedName, baseline, target []sqlitequery.ColumnDefinition, forward, inverse []columnMapping) error {
	base, want := make(map[string]sqlitequery.ColumnDefinition), make(map[string]sqlitequery.ColumnDefinition)
	for _, column := range baseline {
		base[sqliteIdentifierKey(column.Name.Name)] = column
	}
	for _, column := range target {
		want[sqliteIdentifierKey(column.Name.Name)] = column
	}
	seen := make(map[string]struct{})
	for _, mapping := range forward {
		key := sqliteIdentifierKey(mapping.destination.Name)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate forward mapping for %s.%s", displayName(table), mapping.destination.Name)
		}
		seen[key] = struct{}{}
		if _, ok := want[key]; !ok {
			return fmt.Errorf("forward mapping source for unknown %s.%s", displayName(table), mapping.destination.Name)
		}
		if mapping.sourceKind != mappingStagedBackfill && mapping.backfillDecision != "" {
			return fmt.Errorf("backfill decision on non-staged mapping for %s.%s", displayName(table), mapping.destination.Name)
		}
		switch mapping.sourceKind {
		case mappingBaseline, mappingStagedBackfill:
			if mapping.sourceKind == mappingStagedBackfill {
				if sqliteIdentifierKey(mapping.source.Name) != key {
					return fmt.Errorf("staged backfill source does not match destination for %s.%s", displayName(table), mapping.destination.Name)
				}
				if mapping.backfillDecision == "" {
					return fmt.Errorf("staged backfill mapping for %s.%s has no decision", displayName(table), mapping.destination.Name)
				}
			} else if _, ok := base[sqliteIdentifierKey(mapping.source.Name)]; !ok {
				return fmt.Errorf("missing forward source for %s.%s", displayName(table), mapping.destination.Name)
			}
			if mapping.expression != "" {
				return fmt.Errorf("expression on non-default mapping for %s.%s", displayName(table), mapping.destination.Name)
			}
		case mappingTargetDefault:
			if mapping.expression == "" {
				return fmt.Errorf("empty default expression for %s.%s", displayName(table), mapping.destination.Name)
			}
		case mappingNullableNull:
			if mapping.expression != "" {
				return fmt.Errorf("expression on NULL mapping for %s.%s", displayName(table), mapping.destination.Name)
			}
		default:
			return fmt.Errorf("unsupported mapping source kind for %s.%s", displayName(table), mapping.destination.Name)
		}
	}
	for _, column := range target {
		if _, ok := seen[sqliteIdentifierKey(column.Name.Name)]; !ok {
			return fmt.Errorf("missing forward mapping for %s.%s", displayName(table), column.Name.Name)
		}
	}
	seen = make(map[string]struct{})
	sources := make(map[string]struct{})
	for _, mapping := range inverse {
		key, source := sqliteIdentifierKey(mapping.destination.Name), sqliteIdentifierKey(mapping.source.Name)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate inverse mapping for %s.%s", displayName(table), mapping.destination.Name)
		}
		seen[key] = struct{}{}
		if _, ok := base[key]; !ok {
			return fmt.Errorf("inverse mapping destination for unknown %s.%s", displayName(table), mapping.destination.Name)
		}
		if _, ok := want[source]; !ok {
			return fmt.Errorf("missing inverse source for %s.%s", displayName(table), mapping.destination.Name)
		}
		if _, ok := sources[source]; ok {
			return fmt.Errorf("reused inverse source for %s.%s", displayName(table), mapping.source.Name)
		}
		sources[source] = struct{}{}
	}
	for _, column := range baseline {
		if _, ok := seen[sqliteIdentifierKey(column.Name.Name)]; !ok {
			return fmt.Errorf("missing inverse mapping for %s.%s", displayName(table), column.Name.Name)
		}
	}
	return nil
}

func cloneRebuildCarrier(carrier rebuildCarrier) (rebuildCarrier, error) {
	baseline, err := cloneTable(carrier.baseline)
	if err != nil {
		return rebuildCarrier{}, err
	}
	target, err := cloneTable(carrier.target)
	if err != nil {
		return rebuildCarrier{}, err
	}
	baselineIndexes, err := cloneIndexes(carrier.baselineIndexes)
	if err != nil {
		return rebuildCarrier{}, err
	}
	targetIndexes, err := cloneIndexes(carrier.targetIndexes)
	if err != nil {
		return rebuildCarrier{}, err
	}
	return rebuildCarrier{
		tableKey: carrier.tableKey, baseline: baseline, target: target,
		baselineIndexes: baselineIndexes, targetIndexes: targetIndexes,
		forward: append([]columnMapping(nil), carrier.forward...), reverse: append([]columnMapping(nil), carrier.reverse...),
		temporary:        append(sqlitequery.QualifiedName(nil), carrier.temporary...),
		temporaryReverse: append(sqlitequery.QualifiedName(nil), carrier.temporaryReverse...), knownFacts: carrier.knownFacts,
	}, nil
}

func validateSQLiteBackfill(source string, table sqlitequery.QualifiedName, column sqlitequery.Identifier) error {
	tokens := tokenizeSQLite(strings.TrimSpace(source))
	words := make([]sqliteToken, 0, len(tokens))
	for _, token := range tokens {
		if token.text == ";" || token.text == "(" || token.text == ")" {
			continue
		}
		words = append(words, token)
	}
	if len(words) < 5 || !strings.EqualFold(words[0].text, "UPDATE") || !strings.EqualFold(words[2].text, "SET") {
		return fmt.Errorf("source must be one UPDATE assignment targeting %q", column.Name)
	}
	if sqliteIdentifierKey(words[1].text) != sqliteIdentifierKey(table[len(table)-1].Name) {
		return fmt.Errorf("source targets table %q, not %q", words[1].text, displayName(table))
	}
	if sqliteIdentifierKey(words[3].text) != sqliteIdentifierKey(column.Name) || words[4].text != "=" {
		return fmt.Errorf("source must assign only column %q", column.Name)
	}
	for _, token := range words[5:] {
		if token.text == "," || strings.EqualFold(token.text, "FROM") {
			return fmt.Errorf("source contains an unsupported assignment shape")
		}
	}
	return nil
}
