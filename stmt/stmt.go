// Package stmt carries finished SQL text and its bound arguments from
// whatever produced them to whatever executes them.
package stmt

import (
	"bytes"
	"database/sql"

	"github.com/lestrrat-go/rasql/sqltext"
)

// Statement is parameterized SQL ready for execution.
type Statement struct {
	sql  string
	args []any
}

// New pairs SQL text with its bound arguments in placeholder order.
//
// It adopts args rather than copying it, so the caller must not modify a
// slice it passes with "...". Args returns a copy of the slice and supported
// mutable byte values for a caller that needs an inspection copy.
func New(sql sqltext.Text, args ...any) Statement {
	return Statement{sql: string(sql), args: args}
}

// SQL returns the rendered SQL text.
func (s Statement) SQL() string {
	return s.sql
}

// Text returns the rendered SQL as sqltext.Text, which New takes when a caller
// rebuilds a statement from this one. An explicit sqltext.Text conversion
// marks SQL that a program assembled itself, and rasql's own rendered text is
// not that, so a rebuild reads as a rebuild rather than as a fresh claim about
// text nobody parsed.
func (s Statement) Text() sqltext.Text {
	return sqltext.Text(s.sql)
}

// Args returns an inspection copy of the bound arguments in placeholder order.
// It also clones direct []byte values and []byte values inside sql.NamedArg.
func (s Statement) Args() []any {
	args := make([]any, len(s.args))
	for index, value := range s.args {
		args[index] = cloneArg(value)
	}
	return args
}

func cloneArg(value any) any {
	switch value := value.(type) {
	case []byte:
		return bytes.Clone(value)
	case sql.NamedArg:
		value.Value = cloneArg(value.Value)
		return value
	default:
		return value
	}
}

// BoundArgs returns the bound arguments in placeholder order without copying
// them. The returned slice aliases the statement's own storage, so a caller
// that writes to it changes what the statement sends to the database; use
// Args for a copy that is safe to modify. It exists so an execution path can
// hand the arguments straight to database/sql without a per-execution copy.
func (s Statement) BoundArgs() []any {
	return s.args
}
