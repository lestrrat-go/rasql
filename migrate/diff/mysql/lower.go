package mysql

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	mysqlquery "github.com/lestrrat-go/rasql-mysql/query"
	"github.com/lestrrat-go/rasql/migrate/diff"
)

type columnDefinition struct {
	AST   mysqlquery.ColumnDefinition
	Facts columnFacts
}
type loweringEntry struct {
	operation          diff.ProposedOperation
	tableKey           string
	table              mysqlquery.QualifiedName
	baselineColumn     *columnDefinition
	targetColumn       *columnDefinition
	baselineConstraint *mysqlquery.TableConstraint
	targetConstraint   *mysqlquery.TableConstraint
	targetTable        *tableDefinition
	targetIndex        *mysqlquery.CreateIndexStatement
	renameBaseline     mysqlquery.Identifier
	renameTarget       mysqlquery.Identifier
}
type loweringModel struct {
	entries   []loweringEntry
	decisions []diff.RequiredDecision
}

func buildMySQLPlan(baseline, target *schemaSnapshot) (diff.Plan, error) {
	model, err := compareMySQL(baseline, target)
	if err != nil {
		return diff.Plan{}, err
	}
	if len(model.entries) == 0 && len(model.decisions) == 0 {
		return diff.Plan{Dialect: "mysql"}, nil
	}
	operations := make([]diff.ProposedOperation, len(model.entries))
	for i := range model.entries {
		operations[i] = model.entries[i].operation
	}
	ownedModel, err := cloneLoweringModel(model)
	if err != nil {
		return diff.Plan{}, err
	}
	lower := func(resolutions map[string]diff.Resolution) (diff.LoweringResult, error) {
		fresh, err := cloneLoweringModel(ownedModel)
		if err != nil {
			return diff.LoweringResult{}, err
		}
		return lowerMySQL(fresh, resolutions)
	}
	return diff.NewPlan("mysql", operations, model.decisions, lower)
}

func compareMySQL(baseline, target *schemaSnapshot) (loweringModel, error) {
	model := loweringModel{}
	added := make([]diff.SchemaEntry[tableDefinition], 0, len(target.tables))
	for _, key := range sortedTableKeys(target.tables) {
		if _, exists := baseline.tables[key]; !exists {
			added = append(added, diff.SchemaEntry[tableDefinition]{Key: key, Value: target.tables[key]})
		}
	}
	addedOrder, err := orderAddedTables(added, target.lowerCaseTableNames)
	if err != nil {
		return loweringModel{}, fmt.Errorf("mysql schema diff requires manual migration: %w", err)
	}
	for _, key := range addedOrder {
		targetTable := target.tables[key]
		model.entries = append(model.entries, loweringEntry{operation: diff.ProposedOperation{ID: diff.OperationID(diff.OperationCreateTable, "mysql", displayName(targetTable.statement.Name), "", ""), Table: displayName(targetTable.statement.Name), Summary: "create table " + displayName(targetTable.statement.Name), Kind: diff.OperationCreateTable}, tableKey: key, table: targetTable.statement.Name, targetTable: &targetTable})
	}
	for _, key := range sortedTableKeys(target.tables) {
		targetTable := target.tables[key]
		baselineTable, exists := baseline.tables[key]
		if !exists {
			continue
		}
		normalizedBaseline := normalizedTable(baselineTable.statement, baseline.lowerCaseTableNames)
		normalizedTarget := normalizedTable(targetTable.statement, target.lowerCaseTableNames)
		if !reflectTableConstraintsEqual(normalizedBaseline.Constraints, normalizedTarget.Constraints) {
			if err := compareNamedConstraints(&model, targetTable.statement.Name, normalizedBaseline.Constraints, normalizedTarget.Constraints); err != nil {
				return model, err
			}
		}
		baseColumns := map[string]columnDefinition{}
		for _, col := range normalizedBaseline.Columns {
			raw := col
			for _, candidate := range baselineTable.statement.Columns {
				if columnNameKey(candidate.Name.Name) == columnNameKey(col.Name.Name) {
					raw.Name = candidate.Name
					break
				}
			}
			baseColumns[columnNameKey(col.Name.Name)] = columnDefinition{AST: raw, Facts: baselineTable.columns[columnNameKey(col.Name.Name)]}
		}
		targetColumns := map[string]columnDefinition{}
		for _, col := range normalizedTarget.Columns {
			raw := col
			for _, candidate := range targetTable.statement.Columns {
				if columnNameKey(candidate.Name.Name) == columnNameKey(col.Name.Name) {
					raw.Name = candidate.Name
					break
				}
			}
			targetColumns[columnNameKey(col.Name.Name)] = columnDefinition{AST: raw, Facts: targetTable.columns[columnNameKey(col.Name.Name)]}
		}
		var removedColumn, addedColumn *columnDefinition
		for name, col := range baseColumns {
			if _, ok := targetColumns[name]; !ok {
				copy := col
				removedColumn = &copy
			}
		}
		for name, col := range targetColumns {
			if _, ok := baseColumns[name]; !ok {
				copy := col
				addedColumn = &copy
			}
		}
		if removedColumn != nil && addedColumn != nil && sameColumnExceptName(*removedColumn, *addedColumn) {
			model.decisions = append(model.decisions, diff.RequiredDecision{ID: diff.DecisionID(diff.DecisionRename, "mysql", displayName(targetTable.statement.Name), addedColumn.AST.Name.Name), Kind: diff.DecisionRename, Table: displayName(targetTable.statement.Name), Column: addedColumn.AST.Name.Name, Baseline: removedColumn.AST.Name.Name, Target: addedColumn.AST.Name.Name, Reason: "column rename requires caller confirmation"})
		}
		for _, col := range normalizedTarget.Columns {
			key := columnNameKey(col.Name.Name)
			targetCol := targetColumns[key]
			baseCol, exists := baseColumns[key]
			if !exists {
				columnName := targetCol.AST.Name.Name
				entry := loweringEntry{operation: diff.ProposedOperation{ID: diff.OperationID(diff.OperationAddColumn, "mysql", displayName(targetTable.statement.Name), columnName, ""), Table: displayName(targetTable.statement.Name), Column: columnName, Summary: "add column " + displayName(targetTable.statement.Name) + "." + columnName, Kind: diff.OperationAddColumn}, tableKey: key, table: targetTable.statement.Name, targetColumn: &targetCol}
				if removedColumn != nil && addedColumn != nil && sameColumnExceptName(*removedColumn, *addedColumn) && columnNameKey(col.Name.Name) == columnNameKey(addedColumn.AST.Name.Name) {
					entry.renameBaseline = removedColumn.AST.Name
					entry.renameTarget = addedColumn.AST.Name
				}
				model.entries = append(model.entries, entry)
				if columnRequiresBackfill(col) {
					decision := diff.RequiredDecision{ID: diff.DecisionID(diff.DecisionBackfill, "mysql", displayName(targetTable.statement.Name), col.Name.Name), Kind: diff.DecisionBackfill, Table: displayName(targetTable.statement.Name), Column: col.Name.Name, Target: col.Name.Name, Reason: "required column needs an application-specific backfill"}
					model.decisions = append(model.decisions, decision)
				}
				continue
			}
			if sameFullColumn(baseCol, targetCol) {
				continue
			}
			if baseReq, targetReq, ok := sameColumnExceptNullability(baseCol, targetCol); ok && baseReq != targetReq {
				entry := loweringEntry{operation: diff.ProposedOperation{ID: diff.OperationID(diff.OperationAlterNullability, "mysql", displayName(targetTable.statement.Name), col.Name.Name, ""), Table: displayName(targetTable.statement.Name), Column: col.Name.Name, Summary: "alter nullability " + displayName(targetTable.statement.Name) + "." + col.Name.Name, Kind: diff.OperationAlterNullability}, tableKey: key, table: targetTable.statement.Name, baselineColumn: &baseCol, targetColumn: &targetCol}
				model.entries = append(model.entries, entry)
				continue
			}
			return model, fmt.Errorf("mysql schema diff: table %s column %s changed in unsupported fact %s", displayName(targetTable.statement.Name), col.Name.Name, unsupportedColumnFact(baseCol, targetCol))
		}
		for _, col := range normalizedBaseline.Columns {
			if _, ok := targetColumns[columnNameKey(col.Name.Name)]; !ok {
				if removedColumn != nil && addedColumn != nil && columnNameKey(col.Name.Name) == columnNameKey(removedColumn.AST.Name.Name) && sameColumnExceptName(*removedColumn, *addedColumn) {
					continue
				}
				return model, manualMigrationError([]string{fmt.Sprintf("column %s.%s was removed", displayName(baselineTable.statement.Name), col.Name.Name)})
			}
		}
	}
	for _, key := range sortedTableKeys(baseline.tables) {
		if _, ok := target.tables[key]; !ok {
			return model, manualMigrationError([]string{fmt.Sprintf("table %s was removed", displayName(baseline.tables[key].statement.Name))})
		}
	}
	for _, key := range sortedIndexKeys(target.indexes) {
		if _, exists := baseline.indexes[key]; exists {
			continue
		}
		index := target.indexes[key]
		name := displayName(index.statement.Name)
		copy, err := cloneCreateIndexStatement(index.statement)
		if err != nil {
			return model, err
		}
		model.entries = append(model.entries, loweringEntry{operation: diff.ProposedOperation{ID: diff.OperationID(diff.OperationReplaceConstraint, "mysql", displayName(index.statement.Table), "", name), Table: displayName(index.statement.Table), Constraint: name, Summary: "create index " + name, Kind: diff.OperationReplaceConstraint}, table: index.statement.Table, targetIndex: copy})
	}
	for _, base := range baseline.indexes {
		for _, current := range target.indexes {
			if base.statement.Table.String() != current.statement.Table.String() || !strings.EqualFold(base.statement.Name.String(), current.statement.Name.String()) {
				continue
			}
			if !sameIndexElements(base.statement.Elements, current.statement.Elements) || base.statement.Unique != current.statement.Unique {
				return model, fmt.Errorf("mysql schema diff: index %s changed on table %s; manual migration is required", displayName(current.statement.Name), displayName(current.statement.Table))
			}
		}
	}
	sort.SliceStable(model.entries, func(i, j int) bool { return model.entries[i].operation.ID < model.entries[j].operation.ID })
	return model, nil
}

func sameIndexElements(a, b []mysqlquery.IndexElement) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Direction != b[i].Direction || !sameExpression(a[i].Expression, b[i].Expression) {
			return false
		}
	}
	return true
}

func sameFullColumn(left, right columnDefinition) bool {
	return strings.EqualFold(strings.Join(left.AST.Type.Words, " "), strings.Join(right.AST.Type.Words, " ")) && sameExpressionSlices(left.AST.Type.Modifiers, right.AST.Type.Modifiers) && reflectConstraintsEqual(left.AST.Constraints, right.AST.Constraints) && sameFacts(left.Facts, right.Facts)
}
func sameColumnExceptName(left, right columnDefinition) bool {
	left.AST.Name = mysqlquery.Identifier{}
	right.AST.Name = mysqlquery.Identifier{}
	return sameFullColumn(left, right)
}
func sameColumnExceptNullability(left, right columnDefinition) (bool, bool, bool) {
	l := left
	r := right
	l.AST.Constraints = withoutNullability(l.AST.Constraints)
	r.AST.Constraints = withoutNullability(r.AST.Constraints)
	if !normalizedColumnEqual(l.AST, r.AST) || !sameFacts(l.Facts, r.Facts) {
		return false, false, false
	}
	return required(left.AST.Constraints), required(right.AST.Constraints), true
}
func withoutNullability(in []mysqlquery.ColumnConstraint) []mysqlquery.ColumnConstraint {
	out := make([]mysqlquery.ColumnConstraint, 0, len(in))
	for _, c := range in {
		if c.Kind != mysqlquery.ConstraintNotNull && c.Kind != mysqlquery.ConstraintNull {
			out = append(out, c)
		}
	}
	return out
}

func withoutNullDefault(in []mysqlquery.ColumnConstraint) []mysqlquery.ColumnConstraint {
	out := make([]mysqlquery.ColumnConstraint, 0, len(in))
	for _, constraint := range in {
		literal, ok := constraint.Expression.(*mysqlquery.Literal)
		if constraint.Kind == mysqlquery.ConstraintDefault && ok && literal.Kind == mysqlquery.NullLiteral {
			continue
		}
		out = append(out, constraint)
	}
	return out
}

func required(in []mysqlquery.ColumnConstraint) bool {
	for _, c := range in {
		if c.Kind == mysqlquery.ConstraintNotNull {
			return true
		}
	}
	return false
}
func normalizedColumnEqual(a, b mysqlquery.ColumnDefinition) bool {
	return strings.EqualFold(strings.Join(a.Type.Words, " "), strings.Join(b.Type.Words, " ")) && sameExpressionSlices(a.Type.Modifiers, b.Type.Modifiers) && reflectConstraintsEqual(withoutNullability(a.Constraints), withoutNullability(b.Constraints))
}
func sameFacts(a, b columnFacts) bool {
	if a.AutoIncrement != b.AutoIncrement {
		return false
	}
	if (a.Generated == nil) != (b.Generated == nil) {
		return false
	}
	if a.Generated != nil && (a.Generated.Expression != b.Generated.Expression || a.Generated.Storage != b.Generated.Storage) {
		return false
	}
	return qualifiedEqual(a.Collation, b.Collation)
}

func unsupportedColumnFact(a, b columnDefinition) string {
	if !strings.EqualFold(strings.Join(a.AST.Type.Words, " "), strings.Join(b.AST.Type.Words, " ")) || !sameExpressionSlices(a.AST.Type.Modifiers, b.AST.Type.Modifiers) {
		return "type/modifier"
	}
	if !qualifiedEqual(a.Facts.Collation, b.Facts.Collation) {
		return "collation"
	}
	if !sameConstraintKind(a.AST.Constraints, b.AST.Constraints, mysqlquery.ConstraintDefault) {
		return "default"
	}
	if !sameConstraintKind(a.AST.Constraints, b.AST.Constraints, mysqlquery.ConstraintCheck) {
		return "check"
	}
	if (a.Facts.Generated == nil) != (b.Facts.Generated == nil) || a.Facts.Generated != nil && (a.Facts.Generated.Expression != b.Facts.Generated.Expression || a.Facts.Generated.Storage != b.Facts.Generated.Storage) {
		return "generated expression/storage"
	}
	if a.Facts.AutoIncrement != b.Facts.AutoIncrement {
		return "AUTO_INCREMENT"
	}
	return "key"
}

func sameConstraintKind(a, b []mysqlquery.ColumnConstraint, kind mysqlquery.ConstraintKind) bool {
	var left, right []mysqlquery.ColumnConstraint
	for _, constraint := range a {
		if constraint.Kind == kind {
			left = append(left, constraint)
		}
	}
	for _, constraint := range b {
		if constraint.Kind == kind {
			right = append(right, constraint)
		}
	}
	return reflectConstraintsEqual(left, right)
}
func reflectTableConstraintsEqual(a, b []mysqlquery.TableConstraint) bool {
	return reflect.DeepEqual(a, b)
}
func compareNamedConstraints(model *loweringModel, table mysqlquery.QualifiedName, baseline, target []mysqlquery.TableConstraint) error {
	baseNamed := make(map[string]mysqlquery.TableConstraint)
	tarNamed := make(map[string]mysqlquery.TableConstraint)
	for _, c := range baseline {
		if c.Name != nil {
			baseNamed[strings.ToLower(c.Name.Name)] = c
		}
	}
	for _, c := range target {
		if c.Name != nil {
			tarNamed[strings.ToLower(c.Name.Name)] = c
		}
	}
	for name, old := range baseNamed {
		if current, ok := tarNamed[name]; ok && old.Kind != current.Kind {
			return fmt.Errorf("mysql schema diff: named constraint %s on table %s changed kind from %s to %s", name, displayName(table), old.Kind, current.Kind)
		}
	}
	for _, old := range baseline {
		if old.Name == nil {
			continue
		}
		for _, current := range target {
			if current.Name == nil || old.Kind != current.Kind || strings.EqualFold(old.Name.Name, current.Name.Name) {
				continue
			}
			oldCopy, currentCopy := old, current
			oldCopy.Name, currentCopy.Name = nil, nil
			if reflect.DeepEqual(oldCopy, currentCopy) {
				return fmt.Errorf("mysql schema diff: constraint %s on table %s was renamed to %s", old.Name.Name, displayName(table), current.Name.Name)
			}
		}
	}
	key := func(c mysqlquery.TableConstraint) string {
		name := ""
		if c.Name != nil {
			name = strings.ToLower(c.Name.Name)
		}
		return string(c.Kind) + "/" + name
	}
	base := make(map[string]mysqlquery.TableConstraint)
	for _, c := range baseline {
		base[key(c)] = c
	}
	tar := make(map[string]mysqlquery.TableConstraint)
	for _, c := range target {
		tar[key(c)] = c
	}
	for name, c := range tar {
		old, exists := base[name]
		if exists && reflect.DeepEqual(old, c) {
			continue
		}
		if c.Name == nil {
			action := "addition"
			if exists {
				action = "replacement"
			}
			return fmt.Errorf("mysql schema diff: table %s constraints changed: anonymous %s %s is unsupported", displayName(table), c.Kind, action)
		}
		entry := loweringEntry{operation: diff.ProposedOperation{ID: diff.OperationID(diff.OperationReplaceConstraint, "mysql", displayName(table), "", constraintName(c)), Table: displayName(table), Constraint: constraintName(c), Summary: "replace constraint " + constraintName(c), Kind: diff.OperationReplaceConstraint}, table: table}
		if exists {
			oldCopy := old
			entry.baselineConstraint = &oldCopy
		}
		newCopy := c
		entry.targetConstraint = &newCopy
		model.entries = append(model.entries, entry)
	}
	for name, c := range base {
		if _, exists := tar[name]; exists {
			continue
		}
		if c.Name == nil {
			return fmt.Errorf("mysql schema diff: table %s constraints changed: anonymous %s removal is unsupported", displayName(table), c.Kind)
		}
		old := c
		model.entries = append(model.entries, loweringEntry{operation: diff.ProposedOperation{ID: diff.OperationID(diff.OperationReplaceConstraint, "mysql", displayName(table), "", constraintName(c)), Table: displayName(table), Constraint: constraintName(c), Summary: "remove constraint " + constraintName(c), Kind: diff.OperationReplaceConstraint}, table: table, baselineConstraint: &old})
	}
	return nil
}
func constraintName(c mysqlquery.TableConstraint) string {
	if c.Name == nil {
		return ""
	}
	return c.Name.Name
}
func sameExpressionSlices(a, b []mysqlquery.Expression) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameExpression(a[i], b[i]) {
			return false
		}
	}
	return true
}
func reflectConstraintsEqual(a, b []mysqlquery.ColumnConstraint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Kind != b[i].Kind || !reflect.DeepEqual(a[i].Name, b[i].Name) || !reflect.DeepEqual(a[i].References, b[i].References) || !sameExpression(a[i].Expression, b[i].Expression) {
			return false
		}
	}
	return true
}
func sameExpression(a, b mysqlquery.Expression) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	left, lok := a.(*mysqlquery.Literal)
	right, rok := b.(*mysqlquery.Literal)
	if lok || rok {
		return lok && rok && left.Kind == right.Kind && left.Prefix == right.Prefix && left.Value == right.Value
	}
	li, liok := a.(*mysqlquery.IdentifierExpression)
	ri, riok := b.(*mysqlquery.IdentifierExpression)
	if liok || riok {
		if !liok || !riok || len(li.Name) != len(ri.Name) {
			return false
		}
		for i := range li.Name {
			if !strings.EqualFold(li.Name[i].Name, ri.Name[i].Name) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}
func qualifiedEqual(a, b *mysqlquery.QualifiedName) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.String() == b.String()
}

func lowerMySQL(model loweringModel, resolutions map[string]diff.Resolution) (diff.LoweringResult, error) {
	all := make([]mysqlAction, 0)
	owners := make([]mysqlScheduledOperation, len(model.entries))
	irreversible := false
	for i, entry := range model.entries {
		actions, opaque, err := actionsForEntry(model, i, entry, resolutions)
		if err != nil {
			return diff.LoweringResult{}, err
		}
		owners[i] = mysqlScheduledOperation{metadata: entry.operation, actions: actions}
		all = append(all, actions...)
		irreversible = irreversible || opaque
	}
	ordered, err := scheduleMySQLActions(model, all)
	if err != nil {
		return diff.LoweringResult{}, fmt.Errorf("mysql schema diff: dependency cycle involving %w; manual migration required", err)
	}
	return buildMySQLLoweringWithOwners(owners, ordered, irreversible), nil
}

type mysqlStage uint8

const (
	stageCreateTable mysqlStage = iota
	stageDropDependency
	stageAddNullableColumn
	stageBackfill
	stageTransformColumn
	stageAddDependency
	stageCreateIndex
)

type mysqlAction struct {
	operationIndex int
	stage          mysqlStage
	tableKey       string
	objectKey      string
	ordinal        int
	stem           string
	sql            string
	reverseSQL     string
	summary        string
	irreversible   bool
	dependencies   []string
}

type mysqlScheduledOperation struct {
	metadata diff.ProposedOperation
	actions  []mysqlAction
}

func mysqlActionKey(action mysqlAction) string {
	return fmt.Sprintf("%d/%d/%s/%s/%d", action.operationIndex, action.stage, action.tableKey, action.objectKey, action.ordinal)
}

func mysqlSource(position, total int, stem string) string {
	width := 3
	if digits := len(strconv.Itoa(total)); digits > width {
		width = digits
	}
	return fmt.Sprintf("%0*d_%s.sql", width, position+1, filenamePart(stem))
}

func reverseStatement(statement diff.PlannedStatement) diff.PlannedStatement {
	statement.SQL, statement.ReverseSQL = statement.ReverseSQL, statement.SQL
	return statement
}

func actionsForEntry(model loweringModel, operationIndex int, entry loweringEntry, resolutions map[string]diff.Resolution) ([]mysqlAction, bool, error) {
	operation := entry.operation
	tableKey := qualifiedNameKey(entry.table, true)
	object := strings.ToLower(operation.Column + operation.Constraint)
	base := func(stage mysqlStage, stem, sql, reverse, summary string) mysqlAction {
		return mysqlAction{operationIndex: operationIndex, stage: stage, tableKey: tableKey, objectKey: object, stem: stem, sql: sql, reverseSQL: reverse, summary: summary}
	}
	switch operation.Kind {
	case diff.OperationCreateTable:
		if entry.targetTable == nil {
			return nil, false, fmt.Errorf("mysql schema diff: missing table carrier")
		}
		table := entry.targetTable.statement
		parts := make([]string, 0, len(table.Columns)+len(table.Constraints))
		for _, col := range table.Columns {
			part, err := renderFullColumn(columnDefinition{AST: col, Facts: entry.targetTable.columns[columnNameKey(col.Name.Name)]})
			if err != nil {
				return nil, false, err
			}
			parts = append(parts, part)
		}
		for _, constraint := range table.Constraints {
			part, err := renderTableConstraint(constraint)
			if err != nil {
				return nil, false, err
			}
			parts = append(parts, part)
		}
		name := displayName(table.Name)
		action := base(stageCreateTable, "create_table_"+name, "CREATE TABLE "+quoteQualified(table.Name)+" ("+strings.Join(parts, ", ")+");\n", "DROP TABLE "+quoteQualified(table.Name)+";\n", operation.Summary)
		return []mysqlAction{action}, false, nil
	case diff.OperationAddColumn:
		if entry.targetColumn == nil {
			return nil, false, fmt.Errorf("mysql schema diff: missing target column carrier")
		}
		resolution, ok := resolutionForID(model.decisions, operation.Column, resolutions)
		if hasDecision(model.decisions, operation.Column) && !ok {
			return nil, false, fmt.Errorf("missing resolution for %s", operation.Column)
		}
		table := quoteQualified(entry.table)
		if entry.renameBaseline.Name != "" {
			if resolution.RenameFrom != entry.renameBaseline.Name {
				return nil, false, fmt.Errorf("mysql schema diff: rename resolution for %s does not name %s", operation.ID, entry.renameBaseline.Name)
			}
			action := base(stageTransformColumn, "rename_column_"+displayName(entry.table)+"_"+operation.Column, fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s;\n", table, quoteIdentifier(entry.renameBaseline.Name), quoteIdentifier(entry.renameTarget.Name)), fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s;\n", table, quoteIdentifier(entry.renameTarget.Name), quoteIdentifier(entry.renameBaseline.Name)), "rename "+displayName(entry.table)+"."+entry.renameBaseline.Name)
			return []mysqlAction{action}, false, nil
		}
		target := *entry.targetColumn
		full, err := renderFullColumn(target)
		if err != nil {
			return nil, false, err
		}
		reverse := fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;\n", table, quoteIdentifier(operation.Column))
		if required(target.AST.Constraints) && hasDecision(model.decisions, operation.Column) {
			if !ok || strings.TrimSpace(resolution.BackfillSQL) == "" {
				return nil, false, fmt.Errorf("missing resolution for %s", operation.Column)
			}
			final := target
			final.AST.Constraints = withoutNullDefault(final.AST.Constraints)
			finalColumn, renderErr := renderFullColumn(final)
			if renderErr != nil {
				return nil, false, renderErr
			}
			nullable := target
			nullable.AST.Constraints = withoutNullability(nullable.AST.Constraints)
			staged, renderErr := renderFullColumn(nullable)
			if renderErr != nil {
				return nil, false, renderErr
			}
			add := base(stageAddNullableColumn, "add_column_"+displayName(entry.table)+"_"+operation.Column, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;\n", table, staged), reverse, operation.Summary)
			backfill := base(stageBackfill, "backfill_"+displayName(entry.table)+"_"+operation.Column, resolution.BackfillSQL, "", operation.Summary)
			backfill.irreversible = true
			modify := base(stageTransformColumn, "require_column_"+displayName(entry.table)+"_"+operation.Column, fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s;\n", table, finalColumn), fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s;\n", table, staged), operation.Summary)
			modify.dependencies = []string{mysqlActionKey(backfill)}
			backfill.dependencies = []string{mysqlActionKey(add)}
			return []mysqlAction{add, backfill, modify}, true, nil
		}
		action := base(stageAddNullableColumn, "add_column_"+displayName(entry.table)+"_"+operation.Column, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;\n", table, full), reverse, operation.Summary)
		return []mysqlAction{action}, false, nil
	case diff.OperationAlterNullability:
		if entry.targetColumn == nil || entry.baselineColumn == nil {
			return nil, false, fmt.Errorf("mysql schema diff: missing nullability carrier")
		}
		target, err := renderFullColumn(*entry.targetColumn)
		if err != nil {
			return nil, false, err
		}
		baseline, err := renderFullColumn(*entry.baselineColumn)
		if err != nil {
			return nil, false, err
		}
		action := base(stageTransformColumn, "alter_nullability_"+displayName(entry.table)+"_"+operation.Column, fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s;\n", quoteQualified(entry.table), target), fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s;\n", quoteQualified(entry.table), baseline), operation.Summary)
		return []mysqlAction{action}, false, nil
	case diff.OperationReplaceConstraint:
		if entry.targetIndex != nil {
			raw, err := mysqlquery.SerializeStatement(entry.targetIndex)
			if err != nil {
				return nil, false, err
			}
			table, name := quoteQualified(entry.targetIndex.Table), quoteQualified(entry.targetIndex.Name)
			action := base(stageCreateIndex, "create_index_"+displayName(entry.targetIndex.Table)+"_"+displayName(entry.targetIndex.Name), raw+";\n", fmt.Sprintf("DROP INDEX %s ON %s;\n", name, table), operation.Summary)
			columns, err := indexColumns(entry.targetIndex)
			if err != nil {
				return nil, false, err
			}
			action.objectKey = strings.ToLower(displayName(entry.targetIndex.Name))
			action.dependencies = columnActionDependencies(model, tableKey, columns)
			return []mysqlAction{action}, false, nil
		}
		if entry.baselineConstraint == nil {
			addTarget, err := renderConstraintAdd(entry.table, entry.targetConstraint)
			if err != nil {
				return nil, false, err
			}
			dropTarget, err := renderConstraintDrop(entry.table, entry.targetConstraint)
			if err != nil {
				return nil, false, err
			}
			return []mysqlAction{base(stageAddDependency, "add_constraint_"+displayName(entry.table)+"_"+operation.Constraint, addTarget, dropTarget, operation.Summary)}, false, nil
		}
		if entry.targetConstraint == nil {
			dropBase, err := renderConstraintDrop(entry.table, entry.baselineConstraint)
			if err != nil {
				return nil, false, err
			}
			addBase, err := renderConstraintAdd(entry.table, entry.baselineConstraint)
			if err != nil {
				return nil, false, err
			}
			return []mysqlAction{base(stageDropDependency, "drop_constraint_"+displayName(entry.table)+"_"+operation.Constraint, dropBase, addBase, operation.Summary)}, false, nil
		}
		drop, err := renderConstraintDrop(entry.table, entry.baselineConstraint)
		if err != nil {
			return nil, false, err
		}
		addTarget, err := renderConstraintAdd(entry.table, entry.targetConstraint)
		if err != nil {
			return nil, false, err
		}
		addBase, err := renderConstraintAdd(entry.table, entry.baselineConstraint)
		if err != nil {
			return nil, false, err
		}
		dropTarget, err := renderConstraintDrop(entry.table, entry.targetConstraint)
		if err != nil {
			return nil, false, err
		}
		object := strings.ToLower(operation.Constraint)
		dropAction := base(stageDropDependency, "drop_constraint_"+displayName(entry.table)+"_"+operation.Constraint, drop, addBase, operation.Summary)
		dropAction.objectKey = object
		addAction := base(stageAddDependency, "add_constraint_"+displayName(entry.table)+"_"+operation.Constraint, addTarget, dropTarget, operation.Summary)
		addAction.objectKey, addAction.dependencies = object, []string{mysqlActionKey(dropAction)}
		return []mysqlAction{dropAction, addAction}, false, nil
	}
	return nil, false, fmt.Errorf("mysql schema diff: unsupported operation %s", operation.Kind)
}

func resolutionForID(decisions []diff.RequiredDecision, column string, resolutions map[string]diff.Resolution) (diff.Resolution, bool) {
	for _, decision := range decisions {
		if decision.Column == column {
			value, ok := resolutions[decision.ID]
			return value, ok
		}
	}
	return diff.Resolution{}, true
}

func renderConstraintDrop(table mysqlquery.QualifiedName, constraint *mysqlquery.TableConstraint) (string, error) {
	if constraint == nil || constraint.Name == nil {
		return "", fmt.Errorf("mysql schema diff: named constraint is required")
	}
	name := quoteIdentifier(constraint.Name.Name)
	tableSQL := quoteQualified(table)
	switch constraint.Kind {
	case mysqlquery.ConstraintPrimaryKey:
		return fmt.Sprintf("ALTER TABLE %s DROP PRIMARY KEY;\n", tableSQL), nil
	case mysqlquery.ConstraintUnique:
		return fmt.Sprintf("ALTER TABLE %s DROP INDEX %s;\n", tableSQL, name), nil
	case mysqlquery.ConstraintForeignKey:
		return fmt.Sprintf("ALTER TABLE %s DROP FOREIGN KEY %s;\n", tableSQL, name), nil
	case mysqlquery.ConstraintCheck:
		return fmt.Sprintf("ALTER TABLE %s DROP CHECK %s;\n", tableSQL, name), nil
	default:
		return "", fmt.Errorf("mysql schema diff: unsupported constraint kind %s", constraint.Kind)
	}
}

func renderConstraintAdd(table mysqlquery.QualifiedName, constraint *mysqlquery.TableConstraint) (string, error) {
	copy := *constraint
	if copy.Name != nil {
		name := *copy.Name
		name.Quoted = true
		copy.Name = &name
	}
	for i := range copy.Columns {
		copy.Columns[i].Quoted = true
	}
	if copy.References != nil {
		reference := *copy.References
		reference.Table = append(mysqlquery.QualifiedName(nil), reference.Table...)
		for i := range reference.Table {
			reference.Table[i].Quoted = true
		}
		for i := range reference.Columns {
			reference.Columns[i].Quoted = true
		}
		copy.References = &reference
	}
	part, err := renderTableConstraint(copy)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("ALTER TABLE %s ADD %s;\n", quoteQualified(table), part), nil
}

type mysqlColumnRef struct {
	tableKey  string
	columnKey string
}

func constraintColumns(table mysqlquery.QualifiedName, constraints ...*mysqlquery.TableConstraint) []mysqlColumnRef {
	seen := make(map[string]struct{})
	result := make([]mysqlColumnRef, 0)
	for _, constraint := range constraints {
		if constraint == nil {
			continue
		}
		for _, column := range constraint.Columns {
			ref := mysqlColumnRef{tableKey: qualifiedNameKey(table, true), columnKey: columnNameKey(column.Name)}
			key := ref.tableKey + "/" + ref.columnKey
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				result = append(result, ref)
			}
		}
		if constraint.Kind == mysqlquery.ConstraintForeignKey && constraint.References != nil {
			referencedTable := referencedTableName(table, constraint.References.Table)
			for _, column := range constraint.References.Columns {
				ref := mysqlColumnRef{tableKey: qualifiedNameKey(referencedTable, true), columnKey: columnNameKey(column.Name)}
				key := ref.tableKey + "/" + ref.columnKey
				if _, ok := seen[key]; !ok {
					seen[key] = struct{}{}
					result = append(result, ref)
				}
			}
		}
	}
	return result
}

func referencedTableName(owner, referenced mysqlquery.QualifiedName) mysqlquery.QualifiedName {
	if len(referenced) != 1 || len(owner) <= 1 {
		return referenced
	}
	result := make(mysqlquery.QualifiedName, 0, len(owner))
	result = append(result, owner[:len(owner)-1]...)
	result = append(result, referenced...)
	return result
}

func indexColumns(index *mysqlquery.CreateIndexStatement) ([]string, error) {
	result := make([]string, 0, len(index.Elements))
	for _, element := range index.Elements {
		identifier, ok := element.Expression.(*mysqlquery.IdentifierExpression)
		if !ok || len(identifier.Name) == 0 {
			return nil, fmt.Errorf("mysql schema diff: unsupported index %s expression dependency", displayName(index.Name))
		}
		result = append(result, columnNameKey(identifier.Name[len(identifier.Name)-1].Name))
	}
	return result, nil
}

func columnActionDependencies(model loweringModel, tableKey string, columns []string) []string {
	result := make([]string, 0)
	for index, entry := range model.entries {
		if qualifiedNameKey(entry.table, true) != tableKey || entry.operation.Column == "" {
			continue
		}
		for _, column := range columns {
			if columnNameKey(entry.operation.Column) != column {
				continue
			}
			stage := stageTransformColumn
			if entry.operation.Kind == diff.OperationAddColumn {
				stage = stageAddNullableColumn
			}
			result = append(result, mysqlActionKey(mysqlAction{operationIndex: index, stage: stage, tableKey: tableKey, objectKey: strings.ToLower(entry.operation.Column)}))
		}
	}
	return result
}

func scheduleMySQLActions(model loweringModel, actions []mysqlAction) ([]mysqlAction, error) {
	byKey := make(map[string]int, len(actions))
	for i := range actions {
		byKey[mysqlActionKey(actions[i])] = i
	}
	for i := range actions {
		if actions[i].stage == stageCreateTable {
			entry := model.entries[actions[i].operationIndex]
			for _, constraint := range entry.targetTable.statement.Constraints {
				if constraint.Kind != mysqlquery.ConstraintForeignKey || constraint.References == nil {
					continue
				}
				parent := qualifiedNameKey(referencedTableName(entry.table, constraint.References.Table), true)
				for j := range actions {
					if actions[j].stage == stageCreateTable && actions[j].tableKey == parent && actions[j].tableKey != actions[i].tableKey {
						actions[i].dependencies = append(actions[i].dependencies, mysqlActionKey(actions[j]))
					}
				}
			}
		}
	}
	for i := range actions {
		if actions[i].stage != stageDropDependency {
			continue
		}
		entry := model.entries[actions[i].operationIndex]
		columns := constraintColumns(entry.table, entry.baselineConstraint, entry.targetConstraint)
		for j := range actions {
			if actions[j].operationIndex == actions[i].operationIndex {
				continue
			}
			if actions[j].stage == stageAddNullableColumn || actions[j].stage == stageTransformColumn {
				for _, column := range columns {
					if column.tableKey == actions[j].tableKey && column.columnKey == columnNameKey(model.entries[actions[j].operationIndex].operation.Column) {
						actions[j].dependencies = append(actions[j].dependencies, mysqlActionKey(actions[i]))
					}
				}
			}
		}
	}
	for i := range actions {
		if actions[i].stage != stageAddDependency {
			continue
		}
		entry := model.entries[actions[i].operationIndex]
		columns := constraintColumns(entry.table, entry.baselineConstraint, entry.targetConstraint)
		for j := range actions {
			if actions[j].operationIndex == actions[i].operationIndex {
				continue
			}
			if actions[j].stage == stageAddNullableColumn || actions[j].stage == stageTransformColumn {
				for _, column := range columns {
					if column.tableKey == actions[j].tableKey && column.columnKey == columnNameKey(model.entries[actions[j].operationIndex].operation.Column) {
						actions[i].dependencies = append(actions[i].dependencies, mysqlActionKey(actions[j]))
					}
				}
			}
		}
	}
	for i := range actions {
		if actions[i].stage != stageCreateIndex {
			continue
		}
		entry := model.entries[actions[i].operationIndex]
		columns, err := indexColumns(entry.targetIndex)
		if err != nil {
			return nil, err
		}
		for j := range actions {
			if actions[j].tableKey != actions[i].tableKey || actions[j].operationIndex == actions[i].operationIndex {
				continue
			}
			if actions[j].stage == stageAddNullableColumn || actions[j].stage == stageTransformColumn {
				for _, column := range columns {
					if column == columnNameKey(model.entries[actions[j].operationIndex].operation.Column) {
						actions[i].dependencies = append(actions[i].dependencies, mysqlActionKey(actions[j]))
					}
				}
			}
		}
	}
	compare := func(a, b mysqlAction) bool {
		if a.stage != b.stage {
			return a.stage < b.stage
		}
		if a.tableKey != b.tableKey {
			return a.tableKey < b.tableKey
		}
		if a.objectKey != b.objectKey {
			return a.objectKey < b.objectKey
		}
		if a.ordinal != b.ordinal {
			return a.ordinal < b.ordinal
		}
		return mysqlActionKey(a) < mysqlActionKey(b)
	}
	indegree := make(map[string]int, len(actions))
	outgoing := make(map[string][]string, len(actions))
	for _, action := range actions {
		indegree[mysqlActionKey(action)] = 0
	}
	for _, action := range actions {
		for _, dependency := range action.dependencies {
			if _, ok := byKey[dependency]; !ok {
				continue
			}
			indegree[mysqlActionKey(action)]++
			outgoing[dependency] = append(outgoing[dependency], mysqlActionKey(action))
		}
	}
	ready := make([]mysqlAction, 0)
	for _, action := range actions {
		if indegree[mysqlActionKey(action)] == 0 {
			ready = append(ready, action)
		}
	}
	ordered := make([]mysqlAction, 0, len(actions))
	for len(ready) > 0 {
		sort.Slice(ready, func(i, j int) bool { return compare(ready[i], ready[j]) })
		action := ready[0]
		ready = ready[1:]
		ordered = append(ordered, action)
		for _, key := range outgoing[mysqlActionKey(action)] {
			indegree[key]--
			if indegree[key] == 0 {
				ready = append(ready, actions[byKey[key]])
			}
		}
	}
	if len(ordered) != len(actions) {
		remaining := make([]string, 0)
		for key, degree := range indegree {
			if degree > 0 {
				remaining = append(remaining, key)
			}
		}
		sort.Strings(remaining)
		return nil, fmt.Errorf("mysql schema diff: dependency cycle involving %s", strings.Join(remaining, ", "))
	}
	return ordered, nil
}

//nolint:unused // Kept as a package-local lowering compatibility path.
func buildMySQLLowering(model loweringModel, ordered []mysqlAction, irreversible bool) diff.LoweringResult {
	owners := make([]mysqlScheduledOperation, len(model.entries))
	for i, entry := range model.entries {
		owners[i] = mysqlScheduledOperation{metadata: entry.operation}
	}
	for _, action := range ordered {
		owners[action.operationIndex].actions = append(owners[action.operationIndex].actions, action)
	}
	return buildMySQLLoweringWithOwners(owners, ordered, irreversible)
}

func buildMySQLLoweringWithOwners(owners []mysqlScheduledOperation, ordered []mysqlAction, irreversible bool) diff.LoweringResult {
	result := diff.LoweringResult{Operations: make([]diff.ProposedOperation, len(owners))}
	for i, owner := range owners {
		result.Operations[i] = owner.metadata
	}
	irreversibleOwners := make(map[int]struct{})
	for position, action := range ordered {
		forward := diff.PlannedStatement{Source: mysqlSource(position, len(ordered), action.stem), SQL: action.sql, ReverseSQL: action.reverseSQL, Summary: action.summary}
		result.Statements = append(result.Statements, forward)
		result.Operations[action.operationIndex].Forward = append(result.Operations[action.operationIndex].Forward, forward)
		if action.irreversible {
			irreversibleOwners[action.operationIndex] = struct{}{}
		}
	}
	for i := range result.Operations {
		if _, opaque := irreversibleOwners[i]; opaque {
			result.Operations[i].Reverse = nil
			continue
		}
		for j := len(result.Operations[i].Forward) - 1; j >= 0; j-- {
			result.Operations[i].Reverse = append(result.Operations[i].Reverse, reverseStatement(result.Operations[i].Forward[j]))
		}
	}
	if irreversible {
		result.IrreversibleReason = "caller-supplied MySQL backfill has no inferred reverse"
	}
	return result
}
func hasDecision(decisions []diff.RequiredDecision, column string) bool {
	for _, d := range decisions {
		if d.Column == column {
			return true
		}
	}
	return false
}

//nolint:unused // Kept as a package-local resolution compatibility path.
func resolutionFor(decisions []diff.RequiredDecision, column string, resolutions map[string]diff.Resolution) (diff.Resolution, bool) {
	for _, d := range decisions {
		if d.Column == column {
			r, ok := resolutions[d.ID]
			return r, ok
		}
	}
	return diff.Resolution{}, true
}

//nolint:unused // Kept as a package-local rendering compatibility path.
func renderCreateEntry(entry loweringEntry) (diff.PlannedStatement, diff.PlannedStatement, error) {
	if entry.targetTable == nil {
		return diff.PlannedStatement{}, diff.PlannedStatement{}, fmt.Errorf("mysql schema diff: missing table carrier")
	}
	table := entry.targetTable.statement
	columns := make([]string, len(table.Columns))
	for i, col := range table.Columns {
		part, err := renderFullColumn(columnDefinition{AST: col, Facts: entry.targetTable.columns[columnNameKey(col.Name.Name)]})
		if err != nil {
			return diff.PlannedStatement{}, diff.PlannedStatement{}, err
		}
		columns[i] = part
	}
	parts := append([]string{}, columns...)
	for _, constraint := range table.Constraints {
		part, err := renderTableConstraint(constraint)
		if err != nil {
			return diff.PlannedStatement{}, diff.PlannedStatement{}, err
		}
		parts = append(parts, part)
	}
	sql := "CREATE TABLE " + quoteQualified(table.Name) + " (" + strings.Join(parts, ", ") + ");\n"
	name := displayName(table.Name)
	forward := diff.PlannedStatement{Source: "create_table_" + filenamePart(name) + ".sql", SQL: sql, ReverseSQL: "DROP TABLE " + quoteQualified(table.Name) + ";\n", Summary: entry.operation.Summary}
	reverse := diff.PlannedStatement{Source: forward.Source, SQL: forward.ReverseSQL, ReverseSQL: forward.SQL, Summary: forward.Summary}
	return forward, reverse, nil
}

//nolint:unused // Kept as a package-local rendering compatibility path.
func renderIndexEntry(entry loweringEntry) (diff.PlannedStatement, diff.PlannedStatement, error) {
	raw, err := mysqlquery.SerializeStatement(entry.targetIndex)
	if err != nil {
		return diff.PlannedStatement{}, diff.PlannedStatement{}, err
	}
	table := quoteQualified(entry.targetIndex.Table)
	name := quoteQualified(entry.targetIndex.Name)
	forward := diff.PlannedStatement{Source: "create_index_" + filenamePart(displayName(entry.targetIndex.Table)) + "_" + filenamePart(displayName(entry.targetIndex.Name)) + ".sql", SQL: raw + ";\n", ReverseSQL: fmt.Sprintf("DROP INDEX %s ON %s;\n", name, table), Summary: entry.operation.Summary}
	reverse := diff.PlannedStatement{Source: forward.Source, SQL: forward.ReverseSQL, ReverseSQL: forward.SQL, Summary: forward.Summary}
	return forward, reverse, nil
}

//nolint:unused // Kept as a package-local rendering compatibility path.
func renderConstraintEntry(entry loweringEntry) (diff.PlannedStatement, diff.PlannedStatement, error) {
	table := quoteQualified(entry.table)
	drop := func(c *mysqlquery.TableConstraint) string {
		if c == nil {
			return ""
		}
		name := quoteIdentifier(constraintName(*c))
		switch c.Kind {
		case mysqlquery.ConstraintPrimaryKey:
			return fmt.Sprintf("ALTER TABLE %s DROP PRIMARY KEY;\n", table)
		case mysqlquery.ConstraintUnique:
			return fmt.Sprintf("ALTER TABLE %s DROP INDEX %s;\n", table, name)
		case mysqlquery.ConstraintForeignKey:
			return fmt.Sprintf("ALTER TABLE %s DROP FOREIGN KEY %s;\n", table, name)
		case mysqlquery.ConstraintCheck:
			return fmt.Sprintf("ALTER TABLE %s DROP CHECK %s;\n", table, name)
		default:
			return ""
		}
	}
	add := func(c *mysqlquery.TableConstraint) (string, error) {
		if c == nil {
			return "", nil
		}
		part, err := renderTableConstraint(*c)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("ALTER TABLE %s ADD %s;\n", table, part), nil
	}
	forwardAdd, err := add(entry.targetConstraint)
	if err != nil {
		return diff.PlannedStatement{}, diff.PlannedStatement{}, err
	}
	reverseAdd, err := add(entry.baselineConstraint)
	if err != nil {
		return diff.PlannedStatement{}, diff.PlannedStatement{}, err
	}
	forwardSQL := drop(entry.baselineConstraint) + forwardAdd
	reverseSQL := drop(entry.targetConstraint) + reverseAdd
	forward := diff.PlannedStatement{Source: "replace_constraint_" + filenamePart(displayName(entry.table)) + "_" + filenamePart(entry.operation.Constraint) + ".sql", SQL: forwardSQL, ReverseSQL: reverseSQL, Summary: entry.operation.Summary}
	reverse := diff.PlannedStatement{Source: forward.Source, SQL: reverseSQL, ReverseSQL: forwardSQL, Summary: forward.Summary}
	return forward, reverse, nil
}

func renderTableConstraint(constraint mysqlquery.TableConstraint) (string, error) {
	copy := mysqlquery.CreateTableStatement{Persistence: mysqlquery.PermanentRelation, Name: mysqlquery.QualifiedName{{Name: "__rasql_constraint", Quoted: true}}, Constraints: []mysqlquery.TableConstraint{constraint}}
	raw, err := mysqlquery.SerializeStatement(&copy)
	if err != nil {
		return "", fmt.Errorf("mysql schema diff: serialize constraint: %w", err)
	}
	open := strings.Index(raw, "(")
	close := strings.LastIndex(raw, ")")
	if open < 0 || close <= open {
		return "", fmt.Errorf("mysql schema diff: cannot locate serialized constraint")
	}
	return strings.TrimSpace(raw[open+1 : close]), nil
}

//nolint:unused // Kept as a package-local rendering compatibility path.
func renderAddEntry(entry loweringEntry, resolution diff.Resolution) (diff.PlannedStatement, diff.PlannedStatement, error) {
	target := *entry.targetColumn
	column, err := renderFullColumn(target)
	if err != nil {
		return diff.PlannedStatement{}, diff.PlannedStatement{}, err
	}
	table := quoteQualified(entry.table)
	sql := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;\n", table, column)
	if resolution.BackfillSQL != "" && required(target.AST.Constraints) {
		nullable := target
		nullable.AST.Constraints = withoutNullability(nullable.AST.Constraints)
		addColumn, renderErr := renderFullColumn(nullable)
		if renderErr != nil {
			return diff.PlannedStatement{}, diff.PlannedStatement{}, renderErr
		}
		sql = fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;\n%sALTER TABLE %s MODIFY COLUMN %s;\n", table, addColumn, strings.TrimSpace(resolution.BackfillSQL)+"\n", table, column)
	}
	forward := diff.PlannedStatement{Source: "add_column_" + filenamePart(displayName(entry.table)) + "_" + filenamePart(entry.operation.Column) + ".sql", SQL: sql, ReverseSQL: fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;\n", table, quoteIdentifier(entry.operation.Column)), Summary: entry.operation.Summary}
	reverse := diff.PlannedStatement{Source: forward.Source, SQL: forward.ReverseSQL, ReverseSQL: forward.SQL, Summary: forward.Summary}
	return forward, reverse, nil
}

//nolint:unused // Kept as a package-local rendering compatibility path.
func renderRenameEntry(entry loweringEntry, resolution diff.Resolution) (diff.PlannedStatement, diff.PlannedStatement, error) {
	if resolution.RenameFrom == "" || resolution.RenameFrom != entry.renameBaseline.Name {
		return diff.PlannedStatement{}, diff.PlannedStatement{}, fmt.Errorf("mysql schema diff: rename resolution for %s does not name %s", entry.operation.ID, entry.renameBaseline.Name)
	}
	table := quoteQualified(entry.table)
	forwardSQL := fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s;\n", table, quoteIdentifier(entry.renameBaseline.Name), quoteIdentifier(entry.renameTarget.Name))
	reverseSQL := fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s;\n", table, quoteIdentifier(entry.renameTarget.Name), quoteIdentifier(entry.renameBaseline.Name))
	forward := diff.PlannedStatement{Source: "rename_column_" + filenamePart(displayName(entry.table)) + "_" + filenamePart(entry.operation.Column) + ".sql", SQL: forwardSQL, ReverseSQL: reverseSQL, Summary: "rename " + displayName(entry.table) + "." + entry.renameBaseline.Name}
	reverse := diff.PlannedStatement{Source: forward.Source, SQL: reverseSQL, ReverseSQL: forwardSQL, Summary: forward.Summary}
	return forward, reverse, nil
}

//nolint:unused // Kept as a package-local rendering compatibility path.
func renderModifyEntry(entry loweringEntry) (diff.PlannedStatement, diff.PlannedStatement, error) {
	target, err := renderFullColumn(*entry.targetColumn)
	if err != nil {
		return diff.PlannedStatement{}, diff.PlannedStatement{}, err
	}
	base, err := renderFullColumn(*entry.baselineColumn)
	if err != nil {
		return diff.PlannedStatement{}, diff.PlannedStatement{}, err
	}
	table := quoteQualified(entry.table)
	forward := diff.PlannedStatement{Source: "alter_nullability_" + filenamePart(displayName(entry.table)) + "_" + filenamePart(entry.operation.Column) + ".sql", SQL: fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s;\n", table, target), ReverseSQL: fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s;\n", table, base), Summary: entry.operation.Summary}
	reverse := diff.PlannedStatement{Source: forward.Source, SQL: forward.ReverseSQL, ReverseSQL: forward.SQL, Summary: forward.Summary}
	return forward, reverse, nil
}
