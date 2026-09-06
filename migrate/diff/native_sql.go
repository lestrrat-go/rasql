package diff

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	mysqlquery "github.com/lestrrat-go/rasql-mysql/query"
	pgquery "github.com/lestrrat-go/rasql-pg/query"
	sqlitequery "github.com/lestrrat-go/rasql-sqlite/query"
)

func validateNativeSQLSource(dialect, source string) error {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return errors.New("source is empty")
	}
	count, err := parseSupportedNativeSQL(dialect, trimmed)
	if err == nil {
		return requireOneStatement(count)
	}
	if dialect != "sqlite" || !isUnsupportedSQLiteUpdate(err, trimmed) {
		return wrapDialectParseError(dialect, err)
	}
	return validateSQLiteUpdateEnvelope(trimmed)
}

func parseSupportedNativeSQL(dialect, source string) (int, error) {
	switch dialect {
	case "postgresql":
		parsed, err := pgquery.Parse(source)
		if err != nil {
			return 0, err
		}
		return len(parsed.Statements), nil
	case "mysql":
		parsed, err := mysqlquery.Parse(source)
		if err != nil {
			return 0, err
		}
		return len(parsed.Statements), nil
	case "sqlite":
		parsed, err := sqlitequery.Parse(stripLeadingNativeComments(source))
		if err != nil {
			return 0, err
		}
		return len(parsed.Statements), nil
	default:
		return 0, fmt.Errorf("unsupported SQL dialect %q", dialect)
	}
}

func stripLeadingNativeComments(source string) string {
	for {
		start := 0
		for start < len(source) && isNativeSpace(source[start]) {
			start++
		}
		if start+2 <= len(source) && source[start:start+2] == "--" {
			source = source[skipNativeLineComment(source, start+2):]
			continue
		}
		if start+2 <= len(source) && source[start:start+2] == "/*" {
			end, ok := nativeBlockEnd(source, start+2)
			if !ok {
				return source
			}
			source = source[end:]
			continue
		}
		return source[start:]
	}
}

func requireOneStatement(count int) error {
	if count != 1 {
		return fmt.Errorf("source must contain exactly one statement, got %d", count)
	}
	return nil
}

func wrapDialectParseError(dialect string, err error) error {
	label := map[string]string{"postgresql": "PostgreSQL", "mysql": "MySQL", "sqlite": "SQLite"}[dialect]
	if label == "" {
		return fmt.Errorf("invalid SQL: %w", err)
	}
	return fmt.Errorf("invalid %s SQL: %w", label, err)
}

func isUnsupportedSQLiteUpdate(err error, source string) bool {
	var parseErr *sqlitequery.ParseError
	if !errors.As(err, &parseErr) || parseErr.Offset != 0 || parseErr.Message != "unsupported statement" {
		return false
	}
	first, ok := firstNativeWord(source)
	return ok && strings.EqualFold(first, "update")
}

func firstNativeWord(source string) (string, bool) {
	i := 0
	for i < len(source) {
		switch {
		case unicode.IsSpace(rune(source[i])):
			i++
		case i+1 < len(source) && source[i:i+2] == "--":
			i = skipNativeLineComment(source, i+2)
		case i+1 < len(source) && source[i:i+2] == "/*":
			i = skipNativeBlockComment(source, i+2)
		default:
			start := i
			for i < len(source) && nativeWordPart(source[i]) {
				i++
			}
			if start == i {
				return "", false
			}
			return source[start:i], true
		}
	}
	return "", false
}

func validateSQLiteUpdateEnvelope(source string) error {
	depth, tokenCount, setAt := 0, 0, -1
	terminated := false
	for i := 0; i < len(source); {
		if terminated {
			if isNativeSpace(source[i]) {
				i++
				continue
			}
			if i+1 < len(source) && source[i:i+2] == "--" {
				i = skipNativeLineComment(source, i+2)
				continue
			}
			if i+1 < len(source) && source[i:i+2] == "/*" {
				end, ok := nativeBlockEnd(source, i+2)
				if !ok {
					return errors.New("invalid SQLite SQL: unterminated block comment")
				}
				i = end
				continue
			}
			return errors.New("invalid SQLite SQL: multiple statements")
		}
		if isNativeSpace(source[i]) {
			i++
			continue
		}
		if i+1 < len(source) && source[i:i+2] == "--" {
			i = skipNativeLineComment(source, i+2)
			continue
		}
		if i+1 < len(source) && source[i:i+2] == "/*" {
			end, ok := nativeBlockEnd(source, i+2)
			if !ok {
				return errors.New("invalid SQLite SQL: unterminated block comment")
			}
			i = end
			continue
		}
		if source[i] == '\'' || source[i] == '"' || source[i] == '`' || source[i] == '[' {
			end, ok := nativeQuotedEnd(source, i)
			if !ok {
				return errors.New("invalid SQLite SQL: unterminated quoted value")
			}
			tokenCount++
			i = end
			continue
		}
		switch source[i] {
		case '(':
			depth++
			tokenCount++
			i++
			continue
		case ')':
			if depth == 0 {
				return errors.New("invalid SQLite SQL: unexpected closing parenthesis")
			}
			depth--
			tokenCount++
			i++
			continue
		case ';':
			if depth != 0 {
				return errors.New("invalid SQLite SQL: semicolon inside parentheses")
			}
			terminated = true
			i++
			continue
		}
		if nativeWordStart(source[i]) {
			start := i
			for i < len(source) && nativeWordPart(source[i]) {
				i++
			}
			word := source[start:i]
			tokenCount++
			if tokenCount == 1 && !strings.EqualFold(word, "update") {
				return errors.New("invalid SQLite SQL: statement must start with UPDATE")
			}
			if strings.EqualFold(word, "set") && depth == 0 && setAt < 0 {
				setAt = tokenCount
			}
			continue
		}
		tokenCount++
		i++
	}
	if depth != 0 {
		return errors.New("invalid SQLite SQL: unbalanced parentheses")
	}
	if tokenCount < 3 || setAt < 3 || setAt == tokenCount {
		return errors.New("invalid SQLite SQL: UPDATE requires a target and SET expression")
	}
	return nil
}

func nativeQuotedEnd(source string, start int) (int, bool) {
	close := source[start]
	if close == '[' {
		close = ']'
	}
	for i := start + 1; i < len(source); i++ {
		if source[i] != close {
			continue
		}
		if i+1 < len(source) && source[i+1] == close {
			i++
			continue
		}
		return i + 1, true
	}
	return len(source), false
}

func nativeBlockEnd(source string, start int) (int, bool) {
	for i := start; i+1 < len(source); i++ {
		if source[i:i+2] == "*/" {
			return i + 2, true
		}
	}
	return len(source), false
}

func skipNativeBlockComment(source string, start int) int {
	end, _ := nativeBlockEnd(source, start)
	return end
}

func skipNativeLineComment(source string, start int) int {
	for start < len(source) && source[start] != '\n' {
		start++
	}
	return start
}

func isNativeSpace(ch byte) bool { return ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' }

func nativeWordStart(ch byte) bool {
	return ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

func nativeWordPart(ch byte) bool { return nativeWordStart(ch) || ch >= '0' && ch <= '9' }
