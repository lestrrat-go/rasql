// Package sqlscan provides the small lexical SQL scan needed at the native SQL
// boundary. It deliberately does not parse SQL or assign engine semantics to
// expressions.
package sqlscan

import (
	"errors"
	"fmt"
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

// Placeholder identifies an executable placeholder. Number is zero for a
// question mark and the PostgreSQL number for a dollar placeholder.
type Placeholder struct {
	Start  int
	End    int
	Number int
}

// Result is the lexical information needed by native SQL validation and
// rendering. Protected regions include their delimiters.
type Result struct {
	Protected    []Span
	Placeholders []Placeholder
	Semicolons   []int
}

// Scan validates lexical regions and records executable placeholders and
// semicolons. It does not validate statement shape.
func Scan(sql string) (Result, error) {
	var result Result
	for i := 0; i < len(sql); {
		switch {
		case sql[i] == '\'':
			end, err := scanQuoted(sql, i, '\'', true)
			if err != nil {
				return Result{}, err
			}
			result.Protected = append(result.Protected, Span{Start: i, End: end})
			i = end
		case sql[i] == '"':
			end, err := scanQuoted(sql, i, '"', true)
			if err != nil {
				return Result{}, err
			}
			result.Protected = append(result.Protected, Span{Start: i, End: end})
			i = end
		case sql[i] == '`':
			end, err := scanQuoted(sql, i, '`', true)
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
			if number, end, ok := dollarPlaceholder(sql, i); ok {
				result.Placeholders = append(result.Placeholders, Placeholder{Start: i, End: end, Number: number})
				i = end
				continue
			}
			i++
		case sql[i] == '?':
			result.Placeholders = append(result.Placeholders, Placeholder{Start: i, End: i + 1})
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

func dollarPlaceholder(sql string, start int) (int, int, bool) {
	end := start + 1
	if end >= len(sql) || sql[end] < '0' || sql[end] > '9' {
		return 0, 0, false
	}
	for end < len(sql) && sql[end] >= '0' && sql[end] <= '9' {
		end++
	}
	number := 0
	for _, char := range sql[start+1 : end] {
		number = number*10 + int(char-'0')
		if number > 1_000_000_000 {
			return 0, 0, false
		}
	}
	return number, end, true
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
func ValidateSelect(sql string) error {
	scan, err := Scan(sql)
	if err != nil {
		return err
	}
	if len(scan.Semicolons) > 0 {
		return fmt.Errorf("embedded SQL must not contain an executable semicolon")
	}
	first := FirstKeyword(sql, scan)
	if first == "SELECT" {
		return nil
	}
	if first != "WITH" {
		return fmt.Errorf("%w: native result must be a SELECT statement", ErrNotSelect)
	}
	words := ExecutableWords(sql, scan)
	for _, word := range words {
		switch word.Text {
		case "INSERT", "UPDATE", "DELETE":
			return fmt.Errorf("%w: native result WITH bodies must be SELECT statements", ErrNotSelect)
		}
	}
	for _, word := range words[1:] {
		if word.Depth == 0 && word.Text == "SELECT" {
			return nil
		}
	}
	return fmt.Errorf("%w: native result WITH statement must end in SELECT", ErrNotSelect)
}
