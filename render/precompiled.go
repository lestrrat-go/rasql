package render

import (
	"fmt"
	"strings"
)

// Precompiled creates a statement from static, parameterized SQL.
// Callers are responsible for ensuring sql is a trusted compile-time artifact.
func Precompiled(sql string, args ...any) (Statement, error) {
	if strings.TrimSpace(sql) == "" {
		return Statement{}, fmt.Errorf("render: precompiled SQL must not be empty")
	}
	return Statement{sql: sql, args: append([]any(nil), args...)}, nil
}
