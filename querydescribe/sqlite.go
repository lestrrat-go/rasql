package querydescribe

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/lestrrat-go/rasql/internal/dbtype"
	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/schema"
)

type sqliteDescriber struct{ queryer Queryer }

func NewSQLite(queryer Queryer) Describer { return sqliteDescriber{queryer: queryer} }

func (d sqliteDescriber) Describe(ctx context.Context, request Request) (Description, error) {
	if err := validateRequest(request); err != nil {
		return Description{}, err
	}
	if d.queryer == nil {
		return Description{}, fmt.Errorf("%w: %s has nil queryer", ErrInvalidRequest, request.Name)
	}
	if err := oneStatement(request.SQL); err != nil {
		return Description{}, err
	}
	args := make([]any, len(request.Parameters))
	rows, err := d.queryer.QueryContext(ctx, "SELECT * FROM ("+request.SQL+") AS rasql_description LIMIT 0", args...)
	if err != nil {
		return Description{}, err
	}
	defer func() { _ = rows.Close() }()
	types, err := rows.ColumnTypes()
	if err != nil {
		return Description{}, fmt.Errorf("%w: %s column types: %v", ErrIncomplete, request.Name, err)
	}
	if len(types) == 0 {
		return Description{}, fmt.Errorf("%w: %s returned no columns", ErrIncomplete, request.Name)
	}
	result := Description{Cardinality: request.Cardinality, Columns: make([]Column, len(types))}
	fieldNames := make(map[string]string, len(types))
	for i, typ := range types {
		name := typ.Name()
		if !validName(name) {
			return Description{}, fmt.Errorf("%w: %s column %d has invalid name %q", ErrIncomplete, request.Name, i, name)
		}
		for prior := 0; prior < i; prior++ {
			if result.Columns[prior].Name == name {
				return Description{}, fmt.Errorf("%w: %s duplicate column %q", ErrIncomplete, request.Name, name)
			}
		}
		field := generatedFieldName(name)
		if prior, exists := fieldNames[field]; exists {
			return Description{}, fmt.Errorf("%w: %s columns %q and %q collide as generated field %q", ErrIncomplete, request.Name, prior, name, field)
		}
		fieldNames[field] = name
		databaseType := strings.TrimSpace(typ.DatabaseTypeName())
		nullable, ok := typ.Nullable()
		if !ok {
			return Description{}, fmt.Errorf("%w: %s column %d (%s) has no nullability metadata", ErrIncomplete, request.Name, i, name)
		}
		var binding schema.GoBinding
		if databaseType == "" {
			if !countProjection(request.SQL, name, i, len(types)) {
				return Description{}, fmt.Errorf("%w: %s column %d (%s) has no database type", ErrIncomplete, request.Name, i, name)
			}
			binding = schema.GoBinding{Type: "int64"}
			nullable = false
		} else {
			columnType, err := dbtype.SQLite(databaseType)
			if err != nil {
				return Description{}, fmt.Errorf("%w: %s column %d (%s): %v", ErrIncomplete, request.Name, i, name, err)
			}
			resolved, err := schemagen.ResolveGoBinding(schema.ColumnDef{Name: name, Type: columnType, Nullable: nullable})
			if err != nil {
				return Description{}, err
			}
			binding = schema.GoBinding{Type: resolved.Type, NullableType: resolved.NullableType, Imports: append([]schema.GoImport(nil), resolved.Imports...)}
		}
		result.Columns[i] = Column{Name: name, Binding: binding, Nullable: nullable}
	}
	if request.Expected != nil {
		if err := compare(*request.Expected, result, request.Name); err != nil {
			return Description{}, err
		}
	}
	return cloneDescription(result), nil
}

func oneStatement(sqlText string) error {
	clean := stripSQLCommentsAndStrings(sqlText)
	trimmed := strings.TrimSpace(clean)
	if strings.HasSuffix(trimmed, ";") {
		trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, ";"))
	}
	if strings.Contains(trimmed, ";") {
		return fmt.Errorf("%w: SQL contains multiple statements", ErrInvalidRequest)
	}
	if !regexp.MustCompile(`(?i)^select\b`).MatchString(trimmed) {
		return fmt.Errorf("%w: SQL must be one SELECT statement", ErrInvalidRequest)
	}
	return nil
}

func stripSQLCommentsAndStrings(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if i+1 < len(s) && s[i:i+2] == "--" {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			b.WriteByte(' ')
			continue
		}
		if i+1 < len(s) && s[i:i+2] == "/*" {
			i += 2
			for i+1 < len(s) && s[i:i+2] != "*/" {
				i++
			}
			i++
			b.WriteByte(' ')
			continue
		}
		if strings.ContainsRune("'\"`", rune(s[i])) {
			quote := s[i]
			b.WriteByte(' ')
			for i++; i < len(s); i++ {
				if s[i] == quote {
					if i+1 < len(s) && s[i+1] == quote {
						i++
						continue
					}
					break
				}
			}
			continue
		}
		if s[i] == '[' {
			b.WriteByte(' ')
			for i++; i < len(s) && s[i] != ']'; i++ {
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

var countRE = regexp.MustCompile(`(?is)^\s*count\s*\(\s*(\*|(?:[a-z_][a-z0-9_]*|"(?:[^"]|"")*"|` + "`(?:[^`]|``)*`" + `|\[(?:[^\]]|\]\])*\])(?:\s*\.\s*(?:[a-z_][a-z0-9_]*|"(?:[^"]|"")*"|` + "`(?:[^`]|``)*`" + `|\[(?:[^\]]|\]\])*\]))?)\s*\)\s+as\s+([a-z_][a-z0-9_]*|"(?:[^"]|"")*"|` + "`(?:[^`]|``)*`" + `|\[(?:[^\]]|\]\])*\])\s*$`)

func countProjection(sqlText, name string, index, total int) bool {
	if !validCountQueryShape(sqlText) {
		return false
	}
	start, from := topLevelSelectFrom(sqlText)
	if start < 0 || from < 0 {
		return false
	}
	projection := sqlText[start+6 : from]
	parts := splitProjection(projection)
	if len(parts) != total || index >= len(parts) {
		return false
	}
	match := countRE.FindStringSubmatch(stripSQLCommentsAndStringsPreservingQuotes(parts[index]))
	return len(match) == 3 && unquoteIdentifier(match[2]) == name
}

func validCountQueryShape(s string) bool {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "with") {
		return false
	}
	depth, selects, froms := 0, 0, 0
	quote, line, block := byte(0), false, false
	for i := 0; i < len(s); {
		if line {
			if s[i] == '\n' {
				line = false
			}
			i++
			continue
		}
		if block {
			if i+1 < len(s) && s[i:i+2] == "*/" {
				block = false
				i += 2
			} else {
				i++
			}
			continue
		}
		if quote != 0 {
			if s[i] == quote {
				if i+1 < len(s) && s[i+1] == quote {
					i += 2
					continue
				}
				quote = 0
			}
			i++
			continue
		}
		if i+1 < len(s) && s[i:i+2] == "--" {
			line = true
			i += 2
			continue
		}
		if i+1 < len(s) && s[i:i+2] == "/*" {
			block = true
			i += 2
			continue
		}
		if strings.ContainsRune("'\"`", rune(s[i])) {
			quote = s[i]
			i++
			continue
		}
		if s[i] == '[' {
			quote = ']'
			i++
			continue
		}
		if s[i] == '(' {
			depth++
			i++
			continue
		}
		if s[i] == ')' {
			depth--
			if depth < 0 {
				return false
			}
			i++
			continue
		}
		if depth == 0 {
			switch {
			case isWordAt(s, i, "select"):
				selects++
				i += 6
				continue
			case isWordAt(s, i, "from"):
				froms++
				i += 4
				continue
			case isWordAt(s, i, "union"), isWordAt(s, i, "intersect"), isWordAt(s, i, "except"), isWordAt(s, i, "values"):
				return false
			}
		}
		i++
	}
	return quote == 0 && !line && !block && depth == 0 && selects == 1 && froms == 1
}

func splitProjection(s string) []string {
	depth, last := 0, 0
	quote := byte(0)
	lineComment, blockComment := false, false
	var out []string
	for i := 0; i < len(s); i++ {
		if lineComment {
			if s[i] == '\n' {
				lineComment = false
			}
			continue
		}
		if blockComment {
			if i+1 < len(s) && s[i:i+2] == "*/" {
				blockComment = false
				i++
			}
			continue
		}
		if quote != 0 {
			if s[i] == quote {
				if i+1 < len(s) && s[i+1] == quote {
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		if i+1 < len(s) && s[i:i+2] == "--" {
			lineComment = true
			i++
			continue
		}
		if i+1 < len(s) && s[i:i+2] == "/*" {
			blockComment = true
			i++
			continue
		}
		if strings.ContainsRune("'\"`", rune(s[i])) {
			quote = s[i]
			continue
		}
		if s[i] == '[' {
			quote = ']'
			continue
		}
		if s[i] == '(' {
			depth++
		}
		if s[i] == ')' {
			depth--
		}
		if s[i] == ',' && depth == 0 {
			out = append(out, s[last:i])
			last = i + 1
		}
	}
	return append(out, s[last:])
}

func topLevelSelectFrom(s string) (int, int) {
	depth, selectAt, fromAt := 0, -1, -1
	for i := 0; i < len(s); {
		if s[i] == '\'' || s[i] == '"' || s[i] == '`' || s[i] == '[' {
			i = skipQuoted(s, i)
			continue
		}
		if i+1 < len(s) && s[i:i+2] == "--" {
			i = skipLineComment(s, i+2)
			continue
		}
		if i+1 < len(s) && s[i:i+2] == "/*" {
			i = skipBlockComment(s, i+2)
			continue
		}
		if s[i] == '(' {
			depth++
			i++
			continue
		}
		if s[i] == ')' {
			depth--
			i++
			continue
		}
		if depth == 0 && isWordAt(s, i, "select") && selectAt < 0 {
			selectAt = i
			i += 6
			continue
		}
		if depth == 0 && selectAt >= 0 && isWordAt(s, i, "from") {
			fromAt = i
			break
		}
		i++
	}
	return selectAt, fromAt
}

func isWordAt(s string, at int, word string) bool {
	if at+len(word) > len(s) || !strings.EqualFold(s[at:at+len(word)], word) {
		return false
	}
	boundary := func(c byte) bool {
		return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
	}
	return (at == 0 || !boundary(s[at-1])) && (at+len(word) == len(s) || !boundary(s[at+len(word)]))
}

func skipQuoted(s string, at int) int {
	quote := s[at]
	closeQuote := quote
	if quote == '[' {
		closeQuote = ']'
	}
	for i := at + 1; i < len(s); i++ {
		if s[i] == closeQuote {
			if i+1 < len(s) && s[i+1] == closeQuote {
				i++
				continue
			}
			return i + 1
		}
	}
	return len(s)
}
func skipLineComment(s string, at int) int {
	for at < len(s) && s[at] != '\n' {
		at++
	}
	return at
}
func skipBlockComment(s string, at int) int {
	for at+1 < len(s) && s[at:at+2] != "*/" {
		at++
	}
	if at+1 < len(s) {
		return at + 2
	}
	return len(s)
}

func stripSQLCommentsAndStringsPreservingQuotes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if i+1 < len(s) && s[i:i+2] == "--" {
			i = skipLineComment(s, i+2)
			b.WriteByte(' ')
			continue
		}
		if i+1 < len(s) && s[i:i+2] == "/*" {
			i = skipBlockComment(s, i+2)
			b.WriteByte(' ')
			continue
		}
		if s[i] == '\'' {
			i = skipQuoted(s, i)
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func unquoteIdentifier(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return s
	}
	if s[0] == '[' && s[len(s)-1] == ']' {
		return strings.ReplaceAll(s[1:len(s)-1], "]]", "]")
	}
	if strings.ContainsRune("\"`", rune(s[0])) && s[len(s)-1] == s[0] {
		return strings.ReplaceAll(s[1:len(s)-1], string([]byte{s[0], s[0]}), string(s[0]))
	}
	return s
}

func generatedFieldName(name string) string {
	var b strings.Builder
	upper := true
	for _, r := range name {
		if r == '_' || r == '-' || r == ' ' {
			upper = true
			continue
		}
		if upper {
			b.WriteString(strings.ToUpper(string(r)))
			upper = false
		} else {
			b.WriteRune(r)
		}
	}
	result := b.String()
	for suffix, replacement := range map[string]string{"Id": "ID", "Url": "URL", "Uri": "URI", "Http": "HTTP", "Api": "API"} {
		if strings.HasSuffix(result, suffix) {
			result = strings.TrimSuffix(result, suffix) + replacement
		}
	}
	return result
}
