package dialect

import "github.com/lestrrat-go/rasql/query"

// Emitter writes trusted SQL syntax and delegates identifiers, arguments, and
// child expressions to rasql's renderer. An Emitter is valid only during the
// compiler callback and must not be retained.
type Emitter interface {
	WriteSQL(string)
	Identifier(...string) error
	Argument(any) error
	Expression(query.Expression) error
}

// Compiler extends the common renderer for a dialect. Returning handled=false
// from CompileExpression delegates to rasql's built-in expression walker.
type Compiler interface {
	CompilePagination(Emitter, Pagination) error
	CompileExpression(Emitter, query.Expression) (handled bool, err error)
}

// Pagination describes the values and presence of a SELECT's trailing page.
// A present nil value remains a present SQL argument.
type Pagination struct {
	Limit     any
	Offset    any
	HasLimit  bool
	HasOffset bool
}

// CompilerProvider optionally supplies a dialect compiler. Existing dialects
// remain source compatible because Dialect does not embed this interface.
type CompilerProvider interface {
	Compiler() Compiler
}
