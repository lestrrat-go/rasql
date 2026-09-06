package postgresql

import (
	"fmt"
	"strings"
	"unicode"
)

func stripIdentityClauses(source string) (string, error) {
	result := []byte(source)
	depth, inCreateTable := 0, false
	for index := 0; index < len(source); {
		if source[index] == '-' && index+1 < len(source) && source[index+1] == '-' {
			index = skipLine(source, index)
			continue
		}
		if source[index] == '/' && index+1 < len(source) && source[index+1] == '*' {
			index = skipBlock(source, index)
			continue
		}
		if strings.ContainsRune("'\"`", rune(source[index])) {
			index = skipQuote(source, index, source[index])
			continue
		}
		if source[index] == '$' {
			if end := skipDollar(source, index); end > index {
				index = end
				continue
			}
		}
		if source[index] == '(' {
			depth++
			index++
			continue
		}
		if source[index] == ')' {
			if depth > 0 {
				depth--
			}
			index++
			continue
		}
		if !wordStart(source[index]) {
			index++
			continue
		}
		start, end := word(source, index)
		if strings.EqualFold(source[start:end], "CREATE") {
			inCreateTable = strings.EqualFold(nextWord(source, end), "TABLE")
		}
		if inCreateTable && depth > 0 && strings.EqualFold(source[start:end], "GENERATED") {
			clauseEnd, ok := identityEnd(source, end)
			if ok {
				if next := spaces(source, clauseEnd); next < len(source) && source[next] == '(' {
					return "", fmt.Errorf("postgresql schema: identity option lists require a manual migration")
				}
				for offset := start; offset < clauseEnd; offset++ {
					if result[offset] != '\n' && result[offset] != '\r' {
						result[offset] = ' '
					}
				}
				index = clauseEnd
				continue
			}
		}
		index = end
	}
	return string(result), nil
}

func identityEnd(source string, index int) (int, bool) {
	words := []string{"ALWAYS", "AS", "IDENTITY"}
	if strings.EqualFold(nextWord(source, index), "BY") {
		words = []string{"BY", "DEFAULT", "AS", "IDENTITY"}
	}
	for _, expected := range words {
		index = spaces(source, index)
		start, end := word(source, index)
		if !strings.EqualFold(source[start:end], expected) {
			return index, false
		}
		index = end
	}
	return index, true
}
func nextWord(source string, index int) string {
	index = spaces(source, index)
	start, end := word(source, index)
	return source[start:end]
}
func word(source string, index int) (int, int) {
	start := index
	for index < len(source) && (unicode.IsLetter(rune(source[index])) || unicode.IsDigit(rune(source[index])) || source[index] == '_') {
		index++
	}
	return start, index
}
func wordStart(value byte) bool { return unicode.IsLetter(rune(value)) || value == '_' }
func spaces(source string, index int) int {
	for index < len(source) && unicode.IsSpace(rune(source[index])) {
		index++
	}
	return index
}
func skipLine(source string, index int) int {
	for index < len(source) && source[index] != '\n' {
		index++
	}
	return index
}
func skipBlock(source string, index int) int {
	index += 2
	for index+1 < len(source) && source[index:index+2] != "*/" {
		index++
	}
	if index+1 < len(source) {
		index += 2
	}
	return index
}
func skipQuote(source string, index int, quote byte) int {
	index++
	for index < len(source) {
		if source[index] == quote {
			if index+1 < len(source) && source[index+1] == quote {
				index += 2
				continue
			}
			return index + 1
		}
		index++
	}
	return index
}
func skipDollar(source string, index int) int {
	end := strings.IndexByte(source[index+1:], '$')
	if end < 0 {
		return 0
	}
	tag := source[index : index+end+2]
	close := strings.Index(source[index+end+2:], tag)
	if close < 0 {
		return 0
	}
	return index + end + 2 + close + len(tag)
}
