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
	copy, err := cloneTableDefinition(table)
	if err != nil {
		return "", err
	}
	copy.statement.IfNotExists = false
	sql, err := serialize(copy.statement)
	if err != nil {
		return "", err
	}
	return restorePostgreSQLFacts(sql, copy.statement.Name, copy.identities, copy.foreignKeys)
}

func renderColumnDefinition(table tableDefinition, column pgquery.ColumnDefinition) (string, error) {
	statement := &pgquery.CreateTableStatement{Name: table.statement.Name, Persistence: table.statement.Persistence, Columns: []pgquery.ColumnDefinition{column}}
	text, err := serialize(statement)
	if err != nil {
		return "", err
	}
	identities := map[string]identityMode{}
	if mode, ok := table.identities[identifierKey(column.Name)]; ok {
		identities[identifierKey(column.Name)] = mode
	}
	foreignKeys := map[foreignKeyKey]foreignKeyActions{}
	key := foreignKeyKey{table: qualifiedNameKey(table.statement.Name), constraint: identifierKey(column.Name), inline: true}
	if actions, ok := table.foreignKeys[key]; ok {
		foreignKeys[key] = actions
	}
	text, err = restorePostgreSQLFacts(text, table.statement.Name, identities, foreignKeys)
	if err != nil {
		return "", err
	}
	return tableBodyFragment(text)
}

func renderReference(reference *pgquery.Reference, actions foreignKeyActions) (string, error) {
	if reference == nil {
		return "", fmt.Errorf("postgresql schema diff: missing foreign-key reference")
	}
	return fmt.Sprintf("REFERENCES %s (%s) ON DELETE %s ON UPDATE %s", renderQualifiedName(reference.Table), renderIdentifiers(reference.Columns), actions.onDelete, actions.onUpdate), nil
}

func renderTableConstraint(table tableDefinition, constraint pgquery.TableConstraint) (string, error) {
	text, err := serialize(&pgquery.CreateTableStatement{Name: table.statement.Name, Persistence: table.statement.Persistence, Constraints: []pgquery.TableConstraint{constraint}})
	if err != nil {
		return "", err
	}
	foreignKeys := map[foreignKeyKey]foreignKeyActions{}
	if constraint.Name != nil {
		key := foreignKeyKey{table: qualifiedNameKey(table.statement.Name), constraint: identifierKey(*constraint.Name)}
		if actions, ok := table.foreignKeys[key]; ok {
			foreignKeys[key] = actions
		}
	}
	text, err = restorePostgreSQLFacts(text, table.statement.Name, nil, foreignKeys)
	if err != nil {
		return "", err
	}
	return tableBodyFragment(text)
}

func tableBodyFragment(sql string) (string, error) {
	tokens, err := lexPostgreSQL(sql)
	if err != nil {
		return "", err
	}
	open, close := -1, -1
	depth := 0
	for i, token := range tokens {
		if token.text == "(" {
			if open < 0 {
				open = i
			}
			depth++
		}
		if token.text == ")" {
			depth--
			if open >= 0 && depth == 0 {
				close = i
				break
			}
		}
	}
	if open < 0 || close <= open {
		return "", fmt.Errorf("postgresql schema diff: serialized table has no body")
	}
	segments := 0
	segmentDepth := 0
	for i := open + 1; i < close; i++ {
		if tokens[i].text == "(" {
			segmentDepth++
		}
		if tokens[i].text == ")" {
			segmentDepth--
		}
		if tokens[i].text == "," && segmentDepth == 0 {
			segments++
		}
	}
	if segments != 0 {
		return "", fmt.Errorf("postgresql schema diff: serialized fragment contains %d top-level segments", segments+1)
	}
	return strings.TrimSpace(sql[tokens[open].end:tokens[close].start]), nil
}

func restorePostgreSQLFacts(sql string, table pgquery.QualifiedName, identities map[string]identityMode, foreignKeys map[foreignKeyKey]foreignKeyActions) (string, error) {
	tokens, err := lexPostgreSQL(sql)
	if err != nil {
		return "", err
	}
	tableKey := qualifiedNameKey(table)
	insertions := make([]renderInsertion, 0)
	identityUses := map[string]int{}
	foreignKeyUses := map[foreignKeyKey]int{}
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
				addSegmentInsertions(segment, tableKey, identities, foreignKeys, identityUses, foreignKeyUses, &insertions)
			}
			segmentStart = index + 1
		}
	}
	sort.Slice(insertions, func(i, j int) bool { return insertions[i].offset > insertions[j].offset })
	for _, insertion := range insertions {
		sql = sql[:insertion.offset] + insertion.text + sql[insertion.offset:]
	}
	for key := range identities {
		if identityUses[key] != 1 {
			return "", fmt.Errorf("postgresql schema diff: identity fact %s/%s was consumed %d times", tableKey, key, identityUses[key])
		}
	}
	for key := range foreignKeys {
		if foreignKeyUses[key] != 1 {
			return "", fmt.Errorf("postgresql schema diff: foreign-key action fact %s/%s/%t was consumed %d times", key.table, key.constraint, key.inline, foreignKeyUses[key])
		}
	}
	return sql, nil
}

func addSegmentInsertions(segment []sqlToken, tableKey string, identities map[string]identityMode, foreignKeys map[foreignKeyKey]foreignKeyActions, identityUses map[string]int, foreignKeyUses map[foreignKeyKey]int, insertions *[]renderInsertion) {
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
		identityUses[columnKey]++
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
			foreignKeyUses[key]++
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
						foreignKeyUses[key]++
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
