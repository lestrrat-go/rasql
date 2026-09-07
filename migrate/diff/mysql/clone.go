package mysql

import (
	"fmt"
	mysqlquery "github.com/lestrrat-go/rasql-mysql/query"
	"github.com/lestrrat-go/rasql/migrate/diff"
)

func cloneCreateTableStatement(statement *mysqlquery.CreateTableStatement) (*mysqlquery.CreateTableStatement, error) {
	if statement == nil {
		return nil, nil
	}
	raw, err := mysqlquery.SerializeStatement(statement)
	if err != nil {
		return nil, fmt.Errorf("clone CREATE TABLE: %w", err)
	}
	parsed, err := mysqlquery.ParseStatement(raw)
	if err != nil {
		return nil, fmt.Errorf("clone CREATE TABLE: %w", err)
	}
	copy, ok := parsed.(*mysqlquery.CreateTableStatement)
	if !ok {
		return nil, fmt.Errorf("clone CREATE TABLE: parsed %T", parsed)
	}
	return copy, nil
}

func cloneCreateIndexStatement(statement *mysqlquery.CreateIndexStatement) (*mysqlquery.CreateIndexStatement, error) {
	if statement == nil {
		return nil, nil
	}
	raw, err := mysqlquery.SerializeStatement(statement)
	if err != nil {
		return nil, fmt.Errorf("clone CREATE INDEX: %w", err)
	}
	parsed, err := mysqlquery.ParseStatement(raw)
	if err != nil {
		return nil, fmt.Errorf("clone CREATE INDEX: %w", err)
	}
	copy, ok := parsed.(*mysqlquery.CreateIndexStatement)
	if !ok {
		return nil, fmt.Errorf("clone CREATE INDEX: parsed %T", parsed)
	}
	return copy, nil
}

func cloneColumnFacts(facts map[string]columnFacts) map[string]columnFacts {
	if facts == nil {
		return nil
	}
	result := make(map[string]columnFacts, len(facts))
	for key, fact := range facts {
		copy := fact
		if fact.Collation != nil {
			collation := append(mysqlquery.QualifiedName(nil), (*fact.Collation)...)
			copy.Collation = &collation
		}
		if fact.Generated != nil {
			generated := *fact.Generated
			copy.Generated = &generated
		}
		result[key] = copy
	}
	return result
}

func cloneColumnDefinition(column *columnDefinition) (*columnDefinition, error) {
	if column == nil {
		return nil, nil
	}
	table := &mysqlquery.CreateTableStatement{Persistence: mysqlquery.PermanentRelation, Name: mysqlquery.QualifiedName{{Name: "__rasql_clone", Quoted: true}}, Columns: []mysqlquery.ColumnDefinition{column.AST}}
	copy, err := cloneCreateTableStatement(table)
	if err != nil {
		return nil, err
	}
	return &columnDefinition{AST: copy.Columns[0], Facts: cloneColumnFacts(map[string]columnFacts{"column": column.Facts})["column"]}, nil
}

func cloneLoweringModel(model loweringModel) (loweringModel, error) {
	copy := loweringModel{decisions: append([]diff.RequiredDecision(nil), model.decisions...), entries: make([]loweringEntry, len(model.entries))}
	for i, entry := range model.entries {
		copy.entries[i] = entry
		copy.entries[i].operation.Forward = append([]diff.PlannedStatement(nil), entry.operation.Forward...)
		copy.entries[i].operation.Reverse = append([]diff.PlannedStatement(nil), entry.operation.Reverse...)
		copy.entries[i].table = append(mysqlquery.QualifiedName(nil), entry.table...)
		var err error
		copy.entries[i].baselineColumn, err = cloneColumnDefinition(entry.baselineColumn)
		if err != nil {
			return loweringModel{}, err
		}
		copy.entries[i].targetColumn, err = cloneColumnDefinition(entry.targetColumn)
		if err != nil {
			return loweringModel{}, err
		}
		if entry.targetTable != nil {
			table, e := cloneCreateTableStatement(entry.targetTable.statement)
			if e != nil {
				return loweringModel{}, e
			}
			copy.entries[i].targetTable = &tableDefinition{source: entry.targetTable.source, statement: table, columns: cloneColumnFacts(entry.targetTable.columns)}
		}
		copy.entries[i].baselineConstraint, err = cloneConstraint(entry.baselineConstraint)
		if err != nil {
			return loweringModel{}, err
		}
		copy.entries[i].targetConstraint, err = cloneConstraint(entry.targetConstraint)
		if err != nil {
			return loweringModel{}, err
		}
		copy.entries[i].targetIndex, err = cloneCreateIndexStatement(entry.targetIndex)
		if err != nil {
			return loweringModel{}, err
		}
	}
	return copy, nil
}

func cloneConstraint(constraint *mysqlquery.TableConstraint) (*mysqlquery.TableConstraint, error) {
	if constraint == nil {
		return nil, nil
	}
	table := &mysqlquery.CreateTableStatement{Persistence: mysqlquery.PermanentRelation, Name: mysqlquery.QualifiedName{{Name: "__rasql_constraint_clone", Quoted: true}}, Constraints: []mysqlquery.TableConstraint{*constraint}}
	copy, err := cloneCreateTableStatement(table)
	if err != nil {
		return nil, err
	}
	return &copy.Constraints[0], nil
}
