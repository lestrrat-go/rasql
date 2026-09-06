package postgresql

import (
	"fmt"
	"regexp"
	"strings"
)

var identityClause = regexp.MustCompile(`(?is)\bGENERATED\s+(?:ALWAYS|BY\s+DEFAULT)\s+AS\s+IDENTITY\b`)
var noActionClause = regexp.MustCompile(`(?is)\bON\s+(?:DELETE|UPDATE)\s+NO\s+ACTION\b`)
var referenceActionClause = regexp.MustCompile(`(?is)\bON\s+(?:DELETE|UPDATE)\s+(?:CASCADE|RESTRICT|SET\s+(?:NULL|DEFAULT))\b`)

// stripIdentityClauses removes the two bare PostgreSQL identity clauses while
// preserving source offsets for parser diagnostics. Options are deliberately
// rejected until their semantics can be carried through rendering.
func stripIdentityClauses(source string) (string, error) {
	matches := identityClause.FindAllStringIndex(source, -1)
	noActionMatches := noActionClause.FindAllStringIndex(source, -1)
	actionMatches := referenceActionClause.FindAllStringIndex(source, -1)
	if len(matches) == 0 && len(noActionMatches) == 0 && len(actionMatches) == 0 {
		return source, nil
	}
	for _, match := range matches {
		end := match[1]
		for end < len(source) && (source[end] == ' ' || source[end] == '\t' || source[end] == '\r' || source[end] == '\n') {
			end++
		}
		if end < len(source) && source[end] == '(' {
			return "", fmt.Errorf("postgresql schema: identity option lists require a manual migration")
		}
	}
	result := []byte(source)
	for _, match := range matches {
		for index := match[0]; index < match[1]; index++ {
			if result[index] != '\n' && result[index] != '\r' {
				result[index] = ' '
			}
		}
	}
	for _, match := range noActionMatches {
		for index := match[0]; index < match[1]; index++ {
			if result[index] != '\n' && result[index] != '\r' {
				result[index] = ' '
			}
		}
	}
	for _, match := range actionMatches {
		for index := match[0]; index < match[1]; index++ {
			if result[index] != '\n' && result[index] != '\r' {
				result[index] = ' '
			}
		}
	}
	return string(result), nil
}

func identityDiagnostic(source string) string {
	if strings.Contains(strings.ToUpper(source), "GENERATED") {
		return "postgresql schema: identity clause was normalized"
	}
	return ""
}
