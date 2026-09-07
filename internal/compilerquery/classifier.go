package compilerquery

import (
	"fmt"
	"strings"
	"unicode"
)

type Classification struct {
	Operation string
	Returning bool
}

func ClassifySQL(sqlText string) (Classification, error) {
	tokens, err := sqlTokens(sqlText)
	if err != nil {
		return Classification{}, err
	}
	if len(tokens) == 0 {
		return Classification{}, fmt.Errorf("compilerquery: SQL is empty")
	}
	first := strings.ToLower(tokens[0])
	if first == "with" {
		return Classification{}, fmt.Errorf("compilerquery: WITH statements are not accepted")
	}
	if first != "select" && first != "insert" && first != "update" && first != "delete" {
		return Classification{}, fmt.Errorf("compilerquery: statement %q is not accepted", tokens[0])
	}
	classification := Classification{Operation: first}
	depth := 0
	for i, token := range tokens {
		switch token {
		case "(":
			depth++
		case ")":
			depth--
		case ";":
			if i != len(tokens)-1 {
				return Classification{}, fmt.Errorf("compilerquery: multiple statements are not accepted")
			}
		}
		if depth == 0 && first == "insert" && strings.EqualFold(token, "on") && i+1 < len(tokens) && strings.EqualFold(tokens[i+1], "conflict") {
			classification.Operation = "upsert"
		}
		if depth == 0 && first == "insert" && strings.EqualFold(token, "on") && i+2 < len(tokens) && strings.EqualFold(tokens[i+1], "duplicate") && strings.EqualFold(tokens[i+2], "key") {
			classification.Operation = "upsert"
		}
		if depth == 0 && (strings.EqualFold(token, "returning") || strings.EqualFold(token, "return")) {
			classification.Returning = true
		}
	}
	if depth != 0 {
		return Classification{}, fmt.Errorf("compilerquery: unbalanced parentheses")
	}
	return classification, nil
}

func sqlTokens(source string) ([]string, error) {
	var out []string
	for i := 0; i < len(source); {
		if unicode.IsSpace(rune(source[i])) {
			i++
			continue
		}
		if i+1 < len(source) && source[i:i+2] == "--" {
			i += 2
			for i < len(source) && source[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(source) && source[i:i+2] == "/*" {
			end := strings.Index(source[i+2:], "*/")
			if end < 0 {
				return nil, fmt.Errorf("compilerquery: unterminated comment")
			}
			i += end + 4
			continue
		}
		if strings.ContainsRune("'\"`", rune(source[i])) {
			quote := source[i]
			i++
			for i < len(source) {
				if source[i] == quote {
					if i+1 < len(source) && source[i+1] == quote {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			continue
		}
		if strings.ContainsRune("();", rune(source[i])) {
			out = append(out, source[i:i+1])
			i++
			continue
		}
		start := i
		for i < len(source) && !unicode.IsSpace(rune(source[i])) && !strings.ContainsRune("();'\"`", rune(source[i])) {
			i++
		}
		out = append(out, source[start:i])
	}
	return out, nil
}
