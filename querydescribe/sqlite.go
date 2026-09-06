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
	defer rows.Close()
	types, err := rows.ColumnTypes()
	if err != nil {
		return Description{}, fmt.Errorf("%w: %s column types: %v", ErrIncomplete, request.Name, err)
	}
	if len(types) == 0 {
		return Description{}, fmt.Errorf("%w: %s returned no columns", ErrIncomplete, request.Name)
	}
	result := Description{Cardinality: request.Cardinality, Columns: make([]Column, len(types))}
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

func sqliteType(databaseType string) (schema.ColumnType, error) {
	t := strings.ToUpper(strings.TrimSpace(databaseType))
	switch {
	case strings.Contains(t, "DECIMAL") || strings.Contains(t, "NUMERIC"):
		return nil, fmt.Errorf("exact decimal type %q is not exact in SQLite: a NUMERIC-affinity column stores REAL, so declare the column TEXT", databaseType)
	case strings.Contains(t, "BOOL"):
		return schema.BooleanType{}, nil
	case strings.Contains(t, "INT"):
		return schema.IntegerType{}, nil
	case strings.Contains(t, "CHAR") || strings.Contains(t, "CLOB") || strings.Contains(t, "TEXT"):
		return schema.TextType{}, nil
	case strings.Contains(t, "BLOB"):
		return schema.BytesType{}, nil
	case strings.Contains(t, "REAL") || strings.Contains(t, "FLOA") || strings.Contains(t, "DOUB"):
		return schema.FloatType{}, nil
	case strings.Contains(t, "JSON"):
		return schema.JSONType{}, nil
	case strings.Contains(t, "DATE") || strings.Contains(t, "TIME"):
		return schema.TimeType{}, nil
	case strings.Contains(t, "UUID"):
		return schema.UUIDType{}, nil
	default:
		return nil, fmt.Errorf("unsupported sqlite type %q", databaseType)
	}
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

var countRE = regexp.MustCompile(`(?is)^\s*count\s*\(\s*(?:\*|[a-z_][a-z0-9_]*(?:\s*\.\s*[a-z_][a-z0-9_]*)?)\s*\)\s+as\s+([a-z_][a-z0-9_]*)\s*$`)

func countProjection(sqlText, name string, index, total int) bool {
	clean := stripSQLCommentsAndStrings(sqlText)
	upper := strings.ToUpper(clean)
	start := strings.Index(upper, "SELECT")
	if start < 0 {
		return false
	}
	from := strings.Index(upper[start+6:], " FROM")
	if from < 0 {
		return false
	}
	projection := clean[start+6 : start+6+from]
	parts := splitProjection(projection)
	if len(parts) != total || index >= len(parts) {
		return false
	}
	match := countRE.FindStringSubmatch(strings.TrimSpace(parts[index]))
	return len(match) == 2 && match[1] == name
}

func splitProjection(s string) []string {
	depth, last := 0, 0
	var out []string
	for i, r := range s {
		if r == '(' {
			depth++
		}
		if r == ')' {
			depth--
		}
		if r == ',' && depth == 0 {
			out = append(out, s[last:i])
			last = i + 1
		}
	}
	return append(out, s[last:])
}
