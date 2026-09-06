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
	count, err := parseSupportedNativeSQL(dialect, stripLeadingNativeComments(trimmed))
	if err == nil {
		return requireOneStatement(count)
	}
	if !isUnsupportedNativeUpdate(dialect, err, trimmed) {
		return wrapDialectParseError(dialect, err)
	}
	return validateNativeUpdateEnvelope(dialect, trimmed)
}

func parseSupportedNativeSQL(dialect, source string) (int, error) {
	switch dialect {
	case "postgresql":
		parsed, err := pgquery.Parse(source)
		if err != nil {
			if first, ok := firstNativeWord(source); ok && strings.EqualFold(first, "update") {
				return 0, &pgquery.ParseError{Offset: 0, Message: "unsupported statement"}
			}
			return 0, err
		}
		return len(parsed.Statements), nil
	case "mysql":
		parsed, err := mysqlquery.Parse(source)
		if err != nil {
			if first, ok := firstNativeWord(source); ok && strings.EqualFold(first, "update") {
				return 0, &mysqlquery.ParseError{Offset: 0, Message: "unsupported statement"}
			}
			return 0, err
		}
		return len(parsed.Statements), nil
	case "sqlite":
		parsed, err := sqlitequery.Parse(stripLeadingNativeComments(source))
		if err != nil {
			if first, ok := firstNativeWord(source); ok && strings.EqualFold(first, "update") {
				return 0, &sqlitequery.ParseError{Offset: 0, Message: "unsupported statement"}
			}
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

func isUnsupportedNativeUpdate(dialect string, err error, source string) bool {
	var offset int
	var message string
	switch dialect {
	case "postgresql":
		var parseErr *pgquery.ParseError
		if !errors.As(err, &parseErr) {
			return false
		}
		offset, message = parseErr.Offset, parseErr.Message
	case "mysql":
		var parseErr *mysqlquery.ParseError
		if !errors.As(err, &parseErr) {
			return false
		}
		offset, message = parseErr.Offset, parseErr.Message
	case "sqlite":
		var parseErr *sqlitequery.ParseError
		if !errors.As(err, &parseErr) {
			return false
		}
		offset, message = parseErr.Offset, parseErr.Message
	default:
		return false
	}
	first, ok := firstNativeWord(source)
	return ok && strings.EqualFold(first, "update") && offset == 0 && message == "unsupported statement"
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

func validateNativeUpdateEnvelope(dialect, source string) error {
	depth, tokenCount, setAt, equalsAt := 0, 0, -1, -1
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
					return nativeEnvelopeError(dialect, "unterminated block comment")
				}
				i = end
				continue
			}
			if dialect == "mysql" && source[i] == '#' {
				i = skipNativeLineComment(source, i+1)
				continue
			}
			return nativeEnvelopeError(dialect, "multiple statements")
		}
		if isNativeSpace(source[i]) {
			i++
			continue
		}
		if i+1 < len(source) && source[i:i+2] == "--" {
			i = skipNativeLineComment(source, i+2)
			continue
		}
		if dialect == "mysql" && source[i] == '#' {
			i = skipNativeLineComment(source, i+1)
			continue
		}
		if i+1 < len(source) && source[i:i+2] == "/*" {
			end, ok := nativeBlockEnd(source, i+2)
			if !ok {
				return nativeEnvelopeError(dialect, "unterminated block comment")
			}
			i = end
			continue
		}
		if dialect == "postgresql" && source[i] == '$' {
			end, ok := nativeDollarEnd(source, i)
			if !ok {
				return nativeEnvelopeError(dialect, "unterminated dollar-quoted value")
			}
			tokenCount++
			i = end
			continue
		}
		if source[i] == '\'' || source[i] == '"' || (source[i] == '`' && dialect != "postgresql") || (source[i] == '[' && dialect == "sqlite") {
			end, ok := nativeQuotedEndDialect(dialect, source, i)
			if !ok {
				return nativeEnvelopeError(dialect, "unterminated quoted value")
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
				return nativeEnvelopeError(dialect, "unexpected closing parenthesis")
			}
			depth--
			tokenCount++
			i++
			continue
		case ';':
			if depth != 0 {
				return nativeEnvelopeError(dialect, "semicolon inside parentheses")
			}
			terminated = true
			i++
			continue
		}
		if source[i] == '=' && depth == 0 && setAt >= 0 {
			if equalsAt >= 0 {
				return nativeEnvelopeError(dialect, "multiple assignments are unsupported")
			}
			equalsAt = tokenCount + 1
			tokenCount++
			i++
			continue
		}
		if source[i] == ',' && depth == 0 {
			return nativeEnvelopeError(dialect, "multiple assignments are unsupported")
		}
		if nativeWordStart(source[i]) {
			start := i
			for i < len(source) && nativeWordPart(source[i]) {
				i++
			}
			word := source[start:i]
			tokenCount++
			if tokenCount == 1 && !strings.EqualFold(word, "update") {
				return nativeEnvelopeError(dialect, "statement must start with UPDATE")
			}
			if strings.EqualFold(word, "set") && depth == 0 && setAt < 0 {
				setAt = tokenCount
			}
			if setAt >= 0 && depth == 0 && (strings.EqualFold(word, "from") || strings.EqualFold(word, "returning") || strings.EqualFold(word, "order") || strings.EqualFold(word, "limit")) {
				return nativeEnvelopeError(dialect, "unsupported UPDATE clause "+word)
			}
			continue
		}
		tokenCount++
		i++
	}
	if depth != 0 {
		return nativeEnvelopeError(dialect, "unbalanced parentheses")
	}
	if tokenCount < 3 || setAt < 3 || setAt == tokenCount || equalsAt < setAt+2 || tokenCount <= equalsAt {
		return nativeEnvelopeError(dialect, "UPDATE requires a target and assignment expression")
	}
	return nil
}

func nativeEnvelopeError(dialect, message string) error {
	label := map[string]string{"postgresql": "PostgreSQL", "mysql": "MySQL", "sqlite": "SQLite"}[dialect]
	if label == "" {
		label = "SQL"
	}
	return fmt.Errorf("invalid %s SQL: %s", label, message)
}

func nativeDollarEnd(source string, start int) (int, bool) {
	endTag := start + 1
	for endTag < len(source) && (nativeWordPart(source[endTag]) || source[endTag] == '$') {
		if source[endTag] == '$' {
			endTag++
			break
		}
		endTag++
	}
	if endTag <= start+1 || endTag > len(source) || source[endTag-1] != '$' {
		return len(source), false
	}
	tag := source[start:endTag]
	close := strings.Index(source[endTag:], tag)
	if close < 0 {
		return len(source), false
	}
	return endTag + close + len(tag), true
}

func nativeQuotedEnd(source string, start int) (int, bool) {
	return nativeQuotedEndDialect("", source, start)
}

func nativeQuotedEndDialect(dialect, source string, start int) (int, bool) {
	close := source[start]
	if close == '[' {
		close = ']'
	}
	for i := start + 1; i < len(source); i++ {
		if dialect == "mysql" && source[i] == '\\' && close != '`' {
			i++
			continue
		}
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
