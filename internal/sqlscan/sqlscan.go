// Package sqlscan provides the small lexical SQL scan needed at the native SQL
// boundary. It deliberately does not parse SQL or assign engine semantics to
// expressions.
package sqlscan

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

var ErrNotSelect = errors.New("native result is not SELECT")

var ErrInvalidPlaceholder = errors.New("invalid native placeholder")

// Span identifies a protected SQL region. The end offset is exclusive.
type Span struct {
	Start int
	End   int
}

// PlaceholderKind identifies the syntax used by an executable placeholder.
type PlaceholderKind uint8

const (
	Question PlaceholderKind = iota + 1
	Dollar
)

// Placeholder identifies an executable placeholder. Dollar placeholders keep
// their parsed number, including zero, so validation can distinguish $0 from ?.
type Placeholder struct {
	Start   int
	End     int
	Kind    PlaceholderKind
	Number  int
	Invalid bool
}

// Result is the lexical information needed by native SQL validation and
// rendering. Protected regions include their delimiters.
type Result struct {
	Protected    []Span
	Placeholders []Placeholder
	Semicolons   []int
}

// Scan validates lexical regions and records executable placeholders and
// semicolons. It does not validate statement shape. The optional engine name
// selects string-escape rules; omitting it retains the legacy all-engine scan.
func Scan(sql string, engine ...string) (Result, error) {
	engineName := ""
	if len(engine) > 0 {
		engineName = strings.ToLower(strings.TrimSpace(engine[0]))
	}
	backslashStrings := engineName == "" || engineName == "mysql"
	var result Result
	for i := 0; i < len(sql); {
		switch {
		case sql[i] == '\'':
			end, err := scanQuoted(sql, i, '\'', backslashStrings)
			if err != nil {
				return Result{}, err
			}
			result.Protected = append(result.Protected, Span{Start: i, End: end})
			i = end
		case (sql[i] == 'e' || sql[i] == 'E') && i+1 < len(sql) && sql[i+1] == '\'':
			end, err := scanQuoted(sql, i+1, '\'', true)
			if err != nil {
				return Result{}, err
			}
			result.Protected = append(result.Protected, Span{Start: i, End: end})
			i = end
		case sql[i] == '"':
			end, err := scanQuoted(sql, i, '"', engineName == "mysql")
			if err != nil {
				return Result{}, err
			}
			result.Protected = append(result.Protected, Span{Start: i, End: end})
			i = end
		case sql[i] == '`':
			end, err := scanQuoted(sql, i, '`', backslashStrings)
			if err != nil {
				return Result{}, err
			}
			result.Protected = append(result.Protected, Span{Start: i, End: end})
			i = end
		case sql[i] == '-' && i+1 < len(sql) && sql[i+1] == '-':
			end := scanLineComment(sql, i+2)
			result.Protected = append(result.Protected, Span{Start: i, End: end})
			i = end
		case sql[i] == '#':
			end := scanLineComment(sql, i+1)
			result.Protected = append(result.Protected, Span{Start: i, End: end})
			i = end
		case sql[i] == '/' && i+1 < len(sql) && sql[i+1] == '*':
			end, err := scanBlockComment(sql, i)
			if err != nil {
				return Result{}, err
			}
			result.Protected = append(result.Protected, Span{Start: i, End: end})
			i = end
		case sql[i] == '$':
			if end, ok := dollarQuoteStart(sql, i); ok {
				closeStart, closeEnd := strings.Index(sql[end:], sql[i:end]), -1
				if closeStart < 0 {
					return Result{}, fmt.Errorf("unterminated dollar-quoted string")
				}
				closeStart += end
				closeEnd = closeStart + (end - i)
				result.Protected = append(result.Protected, Span{Start: i, End: closeEnd})
				i = closeEnd
				continue
			}
			if number, end, ok, invalid := dollarPlaceholder(sql, i); ok {
				result.Placeholders = append(result.Placeholders, Placeholder{Start: i, End: end, Kind: Dollar, Number: number, Invalid: invalid})
				i = end
				continue
			}
			i++
		case sql[i] == '?':
			result.Placeholders = append(result.Placeholders, Placeholder{Start: i, End: i + 1, Kind: Question})
			i++
		case sql[i] == ';':
			result.Semicolons = append(result.Semicolons, i)
			i++
		default:
			i++
		}
	}
	return result, nil
}

func scanQuoted(sql string, start int, quote byte, escapes bool) (int, error) {
	for i := start + 1; i < len(sql); i++ {
		if escapes && sql[i] == '\\' && i+1 < len(sql) {
			i++
			continue
		}
		if sql[i] != quote {
			continue
		}
		if i+1 < len(sql) && sql[i+1] == quote {
			i++
			continue
		}
		return i + 1, nil
	}
	return 0, fmt.Errorf("unterminated quoted SQL region")
}

func scanLineComment(sql string, start int) int {
	for i := start; i < len(sql); i++ {
		if sql[i] == '\n' || sql[i] == '\r' {
			return i
		}
	}
	return len(sql)
}

func scanBlockComment(sql string, start int) (int, error) {
	depth := 1
	for i := start + 2; i < len(sql); i++ {
		switch {
		case i+1 < len(sql) && sql[i] == '/' && sql[i+1] == '*':
			depth++
			i++
		case i+1 < len(sql) && sql[i] == '*' && sql[i+1] == '/':
			depth--
			i++
			if depth == 0 {
				return i + 1, nil
			}
		}
	}
	return 0, fmt.Errorf("unterminated block comment")
}

func dollarQuoteStart(sql string, start int) (int, bool) {
	end := start + 1
	if end < len(sql) && sql[end] == '$' {
		return end + 1, true
	}
	if end >= len(sql) || !(sql[end] == '_' || unicode.IsLetter(rune(sql[end]))) {
		return 0, false
	}
	end++
	for end < len(sql) && (sql[end] == '_' || unicode.IsLetter(rune(sql[end])) || unicode.IsDigit(rune(sql[end]))) {
		end++
	}
	if end < len(sql) && sql[end] == '$' {
		return end + 1, true
	}
	return 0, false
}

func dollarPlaceholder(sql string, start int) (int, int, bool, bool) {
	end := start + 1
	if end >= len(sql) || sql[end] < '0' || sql[end] > '9' {
		return 0, 0, false, false
	}
	for end < len(sql) && sql[end] >= '0' && sql[end] <= '9' {
		end++
	}
	number, err := strconv.Atoi(sql[start+1 : end])
	if err != nil {
		return 0, end, true, true
	}
	return number, end, true, false
}

// FirstKeyword returns the first executable SQL keyword, ignoring protected
// regions and comments. It returns an empty string for whitespace only input.
func FirstKeyword(sql string, scan Result) string {
	protected := make(map[int]int, len(scan.Protected))
	for _, span := range scan.Protected {
		protected[span.Start] = span.End
	}
	for i := 0; i < len(sql); {
		if end, ok := protected[i]; ok {
			i = end
			continue
		}
		if unicode.IsSpace(rune(sql[i])) {
			i++
			continue
		}
		if !unicode.IsLetter(rune(sql[i])) {
			return ""
		}
		start := i
		for i < len(sql) && (unicode.IsLetter(rune(sql[i])) || unicode.IsDigit(rune(sql[i])) || sql[i] == '_') {
			i++
		}
		return strings.ToUpper(sql[start:i])
	}
	return ""
}

// ExecutableWords returns SQL words outside protected regions with their
// nesting depth. It is intentionally limited to statement-shape validation.
func ExecutableWords(sql string, scan Result) []Word {
	protected := make(map[int]int, len(scan.Protected))
	for _, span := range scan.Protected {
		protected[span.Start] = span.End
	}
	depth := 0
	words := make([]Word, 0)
	for i := 0; i < len(sql); {
		if end, ok := protected[i]; ok {
			i = end
			continue
		}
		switch sql[i] {
		case '(':
			depth++
			i++
			continue
		case ')':
			if depth > 0 {
				depth--
			}
			i++
			continue
		}
		if unicode.IsLetter(rune(sql[i])) {
			start := i
			for i < len(sql) && (unicode.IsLetter(rune(sql[i])) || unicode.IsDigit(rune(sql[i])) || sql[i] == '_') {
				i++
			}
			words = append(words, Word{Text: strings.ToUpper(sql[start:i]), Depth: depth})
			continue
		}
		i++
	}
	return words
}

type Word struct {
	Text  string
	Depth int
}

// ValidateSelect accepts a single SELECT statement, including a WITH clause
// whose bodies are SELECT statements. Data-modifying statements are rejected
// wherever they occur in the statement.
func ValidateSelect(sql string, engine ...string) error {
	scan, err := Scan(sql, engine...)
	if err != nil {
		return err
	}
	if len(scan.Semicolons) > 0 {
		return fmt.Errorf("embedded SQL must not contain an executable semicolon")
	}
	tokens := executableTokens(sql, scan)
	if len(tokens) == 0 {
		return fmt.Errorf("%w: native result must be a SELECT statement", ErrNotSelect)
	}
	if tokens[0].kind == tokenWord && tokens[0].text == "SELECT" {
		return nil
	}
	if tokens[0].kind != tokenWord || tokens[0].text != "WITH" {
		return fmt.Errorf("%w: native result must be a SELECT statement", ErrNotSelect)
	}
	if !validateWithTokens(tokens) {
		return fmt.Errorf("%w: native result WITH bodies must be SELECT statements", ErrNotSelect)
	}
	return nil
}

type tokenKind uint8

const (
	tokenWord tokenKind = iota + 1
	tokenOpen
	tokenClose
	tokenComma
	tokenOpaque
)

type token struct {
	kind tokenKind
	text string
}

func executableTokens(sql string, scan Result) []token {
	protected := make(map[int]Span, len(scan.Protected))
	for _, span := range scan.Protected {
		protected[span.Start] = span
	}
	tokens := make([]token, 0)
	for i := 0; i < len(sql); {
		if span, ok := protected[i]; ok {
			if sql[i] != '-' && sql[i] != '#' && !(sql[i] == '/' && i+1 < len(sql) && sql[i+1] == '*') {
				tokens = append(tokens, token{kind: tokenOpaque, text: sql[i:span.End]})
			}
			i = span.End
			continue
		}
		switch sql[i] {
		case '(':
			tokens = append(tokens, token{kind: tokenOpen, text: "("})
			i++
		case ')':
			tokens = append(tokens, token{kind: tokenClose, text: ")"})
			i++
		case ',':
			tokens = append(tokens, token{kind: tokenComma, text: ","})
			i++
		default:
			if unicode.IsLetter(rune(sql[i])) || sql[i] == '_' {
				start := i
				for i < len(sql) && (unicode.IsLetter(rune(sql[i])) || unicode.IsDigit(rune(sql[i])) || sql[i] == '_') {
					i++
				}
				tokens = append(tokens, token{kind: tokenWord, text: strings.ToUpper(sql[start:i])})
				continue
			}
			i++
		}
	}
	return tokens
}

func validateWithTokens(tokens []token) bool {
	i := 1
	if i < len(tokens) && isWord(tokens[i], "RECURSIVE") {
		i++
	}
	for {
		if i >= len(tokens) || (tokens[i].kind != tokenWord && tokens[i].kind != tokenOpaque) {
			return false
		}
		i++
		if i < len(tokens) && tokens[i].kind == tokenOpen {
			close, ok := matchingParen(tokens, i)
			if !ok {
				return false
			}
			i = close + 1
		}
		if i >= len(tokens) || !isWord(tokens[i], "AS") {
			return false
		}
		i++
		if i < len(tokens) && isWord(tokens[i], "NOT") {
			if i+1 >= len(tokens) || !isWord(tokens[i+1], "MATERIALIZED") {
				return false
			}
			i += 2
		} else if i < len(tokens) && isWord(tokens[i], "MATERIALIZED") {
			i++
		}
		if i >= len(tokens) || tokens[i].kind != tokenOpen {
			return false
		}
		close, ok := matchingParen(tokens, i)
		if !ok || close == i+1 || !validSelectBody(tokens[i+1:close]) {
			return false
		}
		i = close + 1
		if i >= len(tokens) || tokens[i].kind != tokenComma {
			return i < len(tokens) && isWord(tokens[i], "SELECT")
		}
		i++
	}
}

func validSelectBody(tokens []token) bool {
	if len(tokens) == 0 || tokens[0].kind != tokenWord {
		return false
	}
	if tokens[0].text == "SELECT" {
		return true
	}
	if tokens[0].text != "WITH" {
		return false
	}
	return validateWithTokens(tokens)
}

func matchingParen(tokens []token, start int) (int, bool) {
	depth := 0
	for i := start; i < len(tokens); i++ {
		switch tokens[i].kind {
		case tokenOpen:
			depth++
		case tokenClose:
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

func isWord(value token, want string) bool {
	return value.kind == tokenWord && value.text == want
}
