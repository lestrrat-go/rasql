package mysql

import (
	"fmt"
	"strings"

	mysqlquery "github.com/lestrrat-go/rasql-mysql/query"
)

func quoteIdentifier(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }
func quoteQualified(name mysqlquery.QualifiedName) string {
	parts := make([]string, len(name))
	for i, p := range name {
		parts[i] = quoteIdentifier(p.Name)
	}
	return strings.Join(parts, ".")
}

func renderFullColumn(column columnDefinition) (string, error) {
	typeSQL, err := serializeColumnPart(column.AST, nil)
	if err != nil {
		return "", err
	}
	result := quoteIdentifier(column.AST.Name.Name)
	result += " " + typeSQL
	if column.Facts.Collation != nil {
		result += " COLLATE " + quoteQualified(*column.Facts.Collation)
	}
	if column.Facts.Generated != nil {
		result += " GENERATED ALWAYS AS (" + column.Facts.Generated.Expression + ") " + column.Facts.Generated.Storage
	}
	for _, constraint := range column.AST.Constraints {
		if constraint.Kind == mysqlquery.ConstraintNull || constraint.Kind == mysqlquery.ConstraintNotNull || constraint.Kind == mysqlquery.ConstraintDefault {
			part, e := serializeColumnPart(column.AST, &constraint)
			if e != nil {
				return "", e
			}
			result += " " + part
		}
	}
	if column.Facts.AutoIncrement {
		result += " AUTO_INCREMENT"
	}
	for _, constraint := range column.AST.Constraints {
		if constraint.Kind == mysqlquery.ConstraintNull || constraint.Kind == mysqlquery.ConstraintNotNull || constraint.Kind == mysqlquery.ConstraintDefault {
			continue
		}
		part, e := serializeColumnPart(column.AST, &constraint)
		if e != nil {
			return "", e
		}
		result += " " + part
	}
	return result, nil
}

func serializeColumnPart(column mysqlquery.ColumnDefinition, constraint *mysqlquery.ColumnConstraint) (string, error) {
	copy := column
	copy.Constraints = nil
	if constraint != nil {
		copy.Constraints = []mysqlquery.ColumnConstraint{*constraint}
	}
	table := mysqlquery.QualifiedName{{Name: "__rasql_column", Quoted: true}}
	statement := &mysqlquery.AlterTableStatement{Name: table, Actions: []mysqlquery.AlterTableAction{{Kind: mysqlquery.AlterTableAddColumn, Column: &copy}}}
	raw, err := mysqlquery.SerializeStatement(statement)
	if err != nil {
		return "", fmt.Errorf("mysql schema diff: serialize column: %w", err)
	}
	marker := " ADD COLUMN "
	at := strings.Index(raw, marker)
	if at < 0 {
		return "", fmt.Errorf("mysql schema diff: cannot locate serialized column")
	}
	full := strings.TrimSpace(raw[at+len(marker):])
	name := column.Name.Name
	if column.Name.Quoted {
		name = quoteIdentifier(name)
	}
	full = strings.TrimSpace(strings.TrimPrefix(full, name))
	if constraint == nil {
		return full, nil
	}
	typeOnly, err := serializeColumnPart(column, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.TrimPrefix(full, typeOnly)), nil
}
