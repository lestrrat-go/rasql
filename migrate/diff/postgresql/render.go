package postgresql

import (
	"fmt"
	"sort"
	"strings"

	pgquery "github.com/lestrrat-go/rasql-pg/query"
)

type renderInsertion struct {
	offset int
	text   string
}

func renderCreateTable(table tableDefinition) (string, error) {
	copy := cloneTableDefinition(table)
	copy.statement.IfNotExists = false
	sql, err := serialize(copy.statement)
	if err != nil {
		return "", err
	}
	return restorePostgreSQLFacts(sql, copy.statement.Name, copy.identities, copy.foreignKeys)
}

func renderColumnDefinition(table tableDefinition, column pgquery.ColumnDefinition) (string, error) {
	statement := &pgquery.CreateTableStatement{Name: table.statement.Name, Columns: []pgquery.ColumnDefinition{column}}
	text, err := serialize(statement)
	if err != nil {
		return "", err
	}
	return restorePostgreSQLFacts(text, table.statement.Name, table.identities, table.foreignKeys)
}

func renderReference(reference *pgquery.Reference, actions foreignKeyActions) (string, error) {
	if reference == nil {
		return "", fmt.Errorf("postgresql schema diff: missing foreign-key reference")
	}
	return fmt.Sprintf("REFERENCES %s (%s) ON DELETE %s ON UPDATE %s", renderQualifiedName(reference.Table), renderIdentifiers(reference.Columns), actions.onDelete, actions.onUpdate), nil
}

func renderTableConstraint(table tableDefinition, constraint pgquery.TableConstraint) (string, error) {
	text, err := serialize(&pgquery.CreateTableStatement{Name: table.statement.Name, Constraints: []pgquery.TableConstraint{constraint}})
	if err != nil {
		return "", err
	}
	return restorePostgreSQLFacts(text, table.statement.Name, table.identities, table.foreignKeys)
}

func restorePostgreSQLFacts(sql string, table pgquery.QualifiedName, identities map[string]identityMode, foreignKeys map[foreignKeyKey]foreignKeyActions) (string, error) {
	tokens, err := lexPostgreSQL(sql)
	if err != nil {
		return "", err
	}
	tableKey := qualifiedNameKey(table)
	insertions := make([]renderInsertion, 0)
	open := -1
	depth := 0
	for index, token := range tokens {
		if token.text == "(" {
			if open < 0 {
				open = index
			}
			depth++
		}
		if token.text == ")" {
			depth--
		}
		if open >= 0 && depth == 0 {
			break
		}
		_ = index
	}
	if open < 0 {
		return sql, nil
	}
	bodyStart := open + 1
	bodyEnd := len(tokens) - 1
	segmentStart := bodyStart
	segmentDepth := 0
	for index := bodyStart; index <= bodyEnd; index++ {
		if index < bodyEnd && tokens[index].text == "(" {
			segmentDepth++
		}
		if index < bodyEnd && tokens[index].text == ")" {
			segmentDepth--
		}
		if index == bodyEnd || tokens[index].text == "," && segmentDepth == 0 {
			segment := tokens[segmentStart:index]
			if len(segment) > 0 {
				addSegmentInsertions(segment, tableKey, identities, foreignKeys, &insertions)
			}
			segmentStart = index + 1
		}
	}
	sort.Slice(insertions, func(i, j int) bool { return insertions[i].offset > insertions[j].offset })
	for _, insertion := range insertions {
		sql = sql[:insertion.offset] + insertion.text + sql[insertion.offset:]
	}
	return sql, nil
}

func addSegmentInsertions(segment []sqlToken, tableKey string, identities map[string]identityMode, foreignKeys map[foreignKeyKey]foreignKeyActions, insertions *[]renderInsertion) {
	if len(segment) == 0 {
		return
	}
	first, err := scanIdentifier(segment[0])
	if err != nil {
		return
	}
	columnKey := scannedIdentifierKey(first)
	if mode, ok := identities[columnKey]; ok {
		offset := segment[len(segment)-1].end
		depth := 0
		for _, token := range segment {
			if token.text == "(" {
				depth++
			}
			if token.text == ")" {
				depth--
			}
			if depth == 0 && (wordIs(token, "NOT") || wordIs(token, "DEFAULT") || wordIs(token, "REFERENCES") || wordIs(token, "CONSTRAINT") || wordIs(token, "PRIMARY") || wordIs(token, "UNIQUE") || wordIs(token, "CHECK") || wordIs(token, "COLLATE")) {
				offset = token.start
				break
			}
		}
		*insertions = append(*insertions, renderInsertion{offset, " " + string(mode) + " "})
	}
	for index, token := range segment {
		if !wordIs(token, "REFERENCES") {
			continue
		}
		close := -1
		depth := 0
		for cursor := index + 1; cursor < len(segment); cursor++ {
			if segment[cursor].text == "(" {
				depth++
			}
			if segment[cursor].text == ")" {
				depth--
				if depth == 0 {
					close = cursor
					break
				}
			}
		}
		if close < 0 {
			continue
		}
		key := foreignKeyKey{table: tableKey, constraint: columnKey, inline: true}
		if actions, ok := foreignKeys[key]; ok {
			*insertions = append(*insertions, renderInsertion{segment[close].end, " ON DELETE " + string(actions.onDelete) + " ON UPDATE " + string(actions.onUpdate)})
		}
		break
	}
	if wordIs(segment[0], "CONSTRAINT") && len(segment) > 1 {
		name, err := scanIdentifier(segment[1])
		if err != nil {
			return
		}
		key := foreignKeyKey{table: tableKey, constraint: scannedIdentifierKey(name)}
		for index, token := range segment {
			if wordIs(token, "REFERENCES") {
				depth := 0
				close := -1
				for cursor := index + 1; cursor < len(segment); cursor++ {
					if segment[cursor].text == "(" {
						depth++
					}
					if segment[cursor].text == ")" {
						depth--
						if depth == 0 {
							close = cursor
							break
						}
					}
				}
				if close >= 0 {
					if actions, ok := foreignKeys[key]; ok {
						*insertions = append(*insertions, renderInsertion{segment[close].end, " ON DELETE " + string(actions.onDelete) + " ON UPDATE " + string(actions.onUpdate)})
					}
				}
				break
			}
		}
	}
}

func renderQualifiedName(name pgquery.QualifiedName) string {
	parts := make([]string, len(name))
	for i, p := range name {
		parts[i] = renderIdentifier(p)
	}
	return strings.Join(parts, ".")
}
func renderIdentifiers(ids []pgquery.Identifier) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = renderIdentifier(id)
	}
	return strings.Join(parts, ", ")
}
func renderIdentifier(id pgquery.Identifier) string {
	if !id.Quoted {
		return id.Name
	}
	return `"` + strings.ReplaceAll(id.Name, `"`, `""`) + `"`
}

var _ = renderColumnDefinition
var _ = renderReference
var _ = renderTableConstraint
