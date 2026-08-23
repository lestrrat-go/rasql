// Package statement carries finished SQL text and its bound arguments from
// whatever produced them to whatever executes them.
package statement

import "github.com/lestrrat-go/rasql/sqltext"

// Statement is parameterized SQL ready for execution.
type Statement struct {
	sql  string
	args []any
}

// New pairs SQL text with its bound arguments in placeholder order.
//
// It adopts args rather than copying it, so the caller must not modify a
// slice it passes with "...". Args returns a copy for a caller that needs
// one.
func New(sql sqltext.Text, args ...any) Statement {
	return Statement{sql: string(sql), args: args}
}

// SQL returns the rendered SQL text.
func (s Statement) SQL() string {
	return s.sql
}

// Args returns a copy of the bound arguments in placeholder order.
func (s Statement) Args() []any {
	return append([]any(nil), s.args...)
}

// BoundArgs returns the bound arguments in placeholder order without copying
// them. The returned slice aliases the statement's own storage, so a caller
// that writes to it changes what the statement sends to the database; use
// Args for a copy that is safe to modify. It exists so an execution path can
// hand the arguments straight to database/sql without a per-execution copy.
func (s Statement) BoundArgs() []any {
	return s.args
}
