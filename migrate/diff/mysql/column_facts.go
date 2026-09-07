package mysql

import (
	"fmt"
	"strings"
	"unicode"

	mysqlquery "github.com/lestrrat-go/rasql-mysql/query"
)

type columnFacts struct {
	Collation     *mysqlquery.QualifiedName
	Generated     *generatedFact
	AutoIncrement bool
}

type generatedFact struct{ Expression, Storage string }

type scannedTableFacts struct {
	Name    string
	Columns map[string]columnFacts
}

// stripColumnFacts removes parser-unsupported clauses without changing source
// offsets or any text outside a top-level CREATE TABLE column definition.
func stripColumnFacts(source string) (string, []scannedTableFacts, error) {
	tokens, err := lexMySQLSource(source)
	if err != nil {
		return "", nil, err
	}
	tables, ranges, err := scanCreateTableFacts(source, tokens)
	if err != nil {
		return "", nil, err
	}
	masked := []byte(source)
	for _, r := range ranges {
		maskRange(masked, r[0], r[1])
	}
	return string(masked), tables, nil
}

type mysqlLexKind uint8

const (
	mysqlWord mysqlLexKind = iota
	mysqlQuotedIdentifier
	mysqlString
	mysqlPunctuation
)

type mysqlLexToken struct {
	kind              mysqlLexKind
	raw, value        string
	start, end, depth int
}
type mysqlLexState struct {
	quote                     byte
	quoteStart                int
	blockStart                int
	blockComment, lineComment bool
	depth                     int
	parenStarts               []int
}

func lexMySQLSource(source string) ([]mysqlLexToken, error) {
	var tokens []mysqlLexToken
	state := mysqlLexState{}
	for i := 0; i < len(source); {
		if state.lineComment {
			if source[i] == '\n' {
				state.lineComment = false
			}
			i++
			continue
		}
		if state.blockComment {
			if i+1 < len(source) && source[i:i+2] == "*/" {
				state.blockComment = false
				i += 2
			} else {
				i++
			}
			continue
		}
		if state.quote != 0 {
			start := state.quoteStart
			quote := state.quote
			for i < len(source) {
				if source[i] == '\\' {
					i += 2
					continue
				}
				if source[i] == quote {
					if i+1 < len(source) && source[i+1] == quote {
						i += 2
						continue
					}
					i++
					state.quote = 0
					break
				}
				i++
			}
			if state.quote != 0 {
				return nil, fmt.Errorf("mysql schema diff: unterminated %s at byte %d", mysqlQuoteName(quote), start)
			}
			kind := mysqlString
			if quote != '\'' {
				kind = mysqlQuotedIdentifier
			}
			tokens = append(tokens, mysqlLexToken{kind: kind, raw: source[start:i], value: decodeMySQLQuoted(source[start:i], quote), start: start, end: i, depth: state.depth})
			continue
		}
		if source[i] == ' ' || source[i] == '\t' || source[i] == '\r' || source[i] == '\n' {
			i++
			continue
		}
		if i+1 < len(source) && source[i:i+2] == "--" {
			state.lineComment = true
			i += 2
			continue
		}
		if source[i] == '#' {
			state.lineComment = true
			i++
			continue
		}
		if i+1 < len(source) && source[i:i+2] == "/*" {
			state.blockComment, state.blockStart = true, i
			i += 2
			continue
		}
		start := i
		ch := source[i]
		if ch == '\'' || ch == '"' || ch == '`' {
			state.quote, state.quoteStart = ch, i
			i++
			continue
		}
		if ch == '(' {
			tokens = append(tokens, mysqlLexToken{kind: mysqlPunctuation, raw: "(", value: "(", start: i, end: i + 1, depth: state.depth})
			state.depth++
			state.parenStarts = append(state.parenStarts, i)
			i++
			continue
		}
		if ch == ')' {
			if state.depth == 0 {
				return nil, fmt.Errorf("mysql schema diff: unmatched closing parenthesis at byte %d", i)
			}
			state.depth--
			state.parenStarts = state.parenStarts[:len(state.parenStarts)-1]
			tokens = append(tokens, mysqlLexToken{kind: mysqlPunctuation, raw: ")", value: ")", start: i, end: i + 1, depth: state.depth})
			i++
			continue
		}
		if strings.ContainsRune(",.=", rune(ch)) || strings.ContainsRune("+-*/%<>!|&?", rune(ch)) {
			tokens = append(tokens, mysqlLexToken{kind: mysqlPunctuation, raw: source[i : i+1], value: source[i : i+1], start: i, end: i + 1, depth: state.depth})
			i++
			continue
		}
		if isWord(ch) {
			i++
			for i < len(source) && isWord(source[i]) {
				i++
			}
			raw := source[start:i]
			tokens = append(tokens, mysqlLexToken{kind: mysqlWord, raw: raw, value: strings.ToLower(raw), start: start, end: i, depth: state.depth})
			continue
		}
		tokens = append(tokens, mysqlLexToken{kind: mysqlPunctuation, raw: source[i : i+1], value: source[i : i+1], start: i, end: i + 1, depth: state.depth})
		i++
	}
	if state.quote != 0 {
		return nil, fmt.Errorf("mysql schema diff: unterminated %s at byte %d", mysqlQuoteName(state.quote), state.quoteStart)
	}
	if state.blockComment {
		return nil, fmt.Errorf("mysql schema diff: unterminated block comment at byte %d", state.blockStart)
	}
	if state.depth != 0 {
		return nil, fmt.Errorf("mysql schema diff: unclosed parenthesis opened at byte %d", state.parenStarts[len(state.parenStarts)-1])
	}
	return tokens, nil
}

func mysqlQuoteName(q byte) string {
	if q == '\'' {
		return "single-quoted string"
	}
	if q == '`' {
		return "quoted identifier"
	}
	return "double-quoted string"
}
func decodeMySQLQuoted(raw string, quote byte) string {
	if len(raw) < 2 {
		return raw
	}
	value := strings.ReplaceAll(raw[1:len(raw)-1], string([]byte{quote, quote}), string(quote))
	value = strings.ReplaceAll(value, "\\"+string(quote), string(quote))
	return strings.ReplaceAll(value, "\\\\", "\\")
}
func decodeMySQLIdentifier(token mysqlLexToken) (string, error) {
	if token.kind != mysqlWord && token.kind != mysqlQuotedIdentifier {
		return "", fmt.Errorf("invalid identifier")
	}
	return token.value, nil
}

func scanCreateTableFacts(source string, tokens []mysqlLexToken) ([]scannedTableFacts, [][2]int, error) {
	var tables []scannedTableFacts
	var ranges [][2]int
	for i := 0; i+1 < len(tokens); i++ {
		if tokens[i].kind != mysqlWord || tokens[i].value != "create" || tokens[i+1].value != "table" {
			continue
		}
		j := i + 2
		if j+2 < len(tokens) && tokens[j].value == "if" && tokens[j+1].value == "not" && tokens[j+2].value == "exists" {
			j += 3
		}
		if j >= len(tokens) {
			continue
		}
		name, err := decodeMySQLIdentifier(tokens[j])
		if err != nil {
			return nil, nil, err
		}
		j++
		if j+1 < len(tokens) && tokens[j].value == "." {
			part, err := decodeMySQLIdentifier(tokens[j+1])
			if err != nil {
				return nil, nil, err
			}
			name += "." + part
			j += 2
		}
		for j < len(tokens) && tokens[j].value != "(" {
			j++
		}
		if j >= len(tokens) {
			continue
		}
		open := j
		close := -1
		for k := open + 1; k < len(tokens); k++ {
			if tokens[k].value == ")" && tokens[k].depth == tokens[open].depth {
				close = k
				break
			}
		}
		if close < 0 {
			continue
		}
		facts := scannedTableFacts{Name: name, Columns: map[string]columnFacts{}}
		begin := open + 1
		for k := open + 1; k <= close; k++ {
			if k == close || (tokens[k].value == "," && tokens[k].depth == tokens[open].depth+1) {
				end := k
				if begin < end {
					first := tokens[begin]
					col, _ := decodeMySQLIdentifier(first)
					if !isTableClause(col) {
						fact, rs, err := scanColumnFactTokens(source, tokens[begin:end])
						if err != nil {
							return nil, nil, fmt.Errorf("mysql schema diff: table %s column %s: %w", name, col, err)
						}
						if fact.Collation != nil || fact.Generated != nil || fact.AutoIncrement {
							facts.Columns[strings.ToLower(col)] = fact
							ranges = append(ranges, rs...)
						}
					}
				}
				begin = k + 1
			}
		}
		tables = append(tables, facts)
		i = close
	}
	return tables, ranges, nil
}

func scanColumnFactTokens(source string, tokens []mysqlLexToken) (columnFacts, [][2]int, error) {
	var fact columnFacts
	var ranges [][2]int
	var collate, generated, auto bool
	for i := 1; i < len(tokens); i++ {
		t := tokens[i]
		if t.kind != mysqlWord {
			continue
		}
		switch t.value {
		case "collate":
			if collate || i+1 >= len(tokens) {
				if collate {
					return fact, nil, fmt.Errorf("duplicate COLLATE attribute")
				}
				return fact, nil, fmt.Errorf("invalid COLLATE clause")
			}
			collate = true
			name, err := decodeMySQLIdentifier(tokens[i+1])
			if err != nil {
				return fact, nil, fmt.Errorf("invalid COLLATE clause")
			}
			q := mysqlquery.QualifiedName{{Name: name}}
			end := tokens[i+1].end
			if i+3 < len(tokens) && tokens[i+2].value == "." {
				part, partErr := decodeMySQLIdentifier(tokens[i+3])
				if partErr != nil {
					return fact, nil, fmt.Errorf("invalid COLLATE clause")
				}
				q = mysqlquery.QualifiedName{{Name: name}, {Name: part}}
				end = tokens[i+3].end
			}
			fact.Collation = &q
			ranges = append(ranges, [2]int{t.start, end})
			i++
			if i+1 < len(tokens) && tokens[i+1].value == "." {
				i++
			}
		case "auto_increment":
			if auto {
				return fact, nil, fmt.Errorf("duplicate AUTO_INCREMENT attribute")
			}
			auto = true
			fact.AutoIncrement = true
			ranges = append(ranges, [2]int{t.start, t.end})
		case "generated":
			if generated {
				return fact, nil, fmt.Errorf("duplicate GENERATED attribute")
			}
			generated = true
			j := i + 1
			if j < len(tokens) && tokens[j].value == "always" {
				j++
			}
			if j >= len(tokens) || tokens[j].value != "as" {
				return fact, nil, fmt.Errorf("generated column requires AS")
			}
			j++
			if j >= len(tokens) || tokens[j].value != "(" {
				return fact, nil, fmt.Errorf("generated column requires expression")
			}
			open := j
			close := -1
			for j++; j < len(tokens); j++ {
				if tokens[j].value == ")" && tokens[j].depth == tokens[open].depth {
					close = j
					break
				}
			}
			if close < 0 || close == open+1 {
				return fact, nil, fmt.Errorf("generated column requires expression")
			}
			storage := "VIRTUAL"
			end := tokens[close].end
			if close+1 < len(tokens) && (tokens[close+1].value == "virtual" || tokens[close+1].value == "stored") {
				storage = strings.ToUpper(tokens[close+1].value)
				end = tokens[close+1].end
			} else if close+1 < len(tokens) && tokens[close+1].kind == mysqlWord && !mysqlColumnConstraintStarter(tokens[close+1].value) {
				return fact, nil, fmt.Errorf("generated column requires VIRTUAL or STORED")
			}
			fact.Generated = &generatedFact{Expression: strings.TrimSpace(source[tokens[open].end:tokens[close].start]), Storage: storage}
			ranges = append(ranges, [2]int{t.start, end})
			i = close
		}
	}
	defaultAttr := false
	for _, token := range tokens {
		if token.kind == mysqlWord && token.value == "default" && token.depth == tokens[0].depth {
			defaultAttr = true
		}
	}
	if fact.Generated != nil && (fact.AutoIncrement || defaultAttr) {
		return fact, nil, fmt.Errorf("generated column cannot combine with AUTO_INCREMENT or DEFAULT")
	}
	return fact, ranges, nil
}

func mysqlColumnConstraintStarter(word string) bool {
	switch word {
	case "default", "not", "null", "primary", "unique", "key", "references", "check", "constraint", "comment", "collate", "auto_increment":
		return true
	}
	return false
}

func isWord(value byte) bool {
	return value == '_' || unicode.IsLetter(rune(value)) || unicode.IsDigit(rune(value))
}
func isTableClause(value string) bool {
	switch strings.ToUpper(value) {
	case "CONSTRAINT", "PRIMARY", "UNIQUE", "FOREIGN", "CHECK", "KEY", "INDEX":
		return true
	}
	return false
}
func maskRange(value []byte, start, end int) {
	for i := start; i < end; i++ {
		if value[i] != '\n' && value[i] != '\r' {
			value[i] = ' '
		}
	}
}
