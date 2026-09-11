// Package planerr holds the error a plan reports when it cannot be turned into
// SQL. It lives here rather than in the root package so that code under
// internal/ can raise the same error, which code in the root package can
// return unchanged.
package planerr

// Error reports a plan that cannot be compiled. Code names the kind of
// failure, Path points at the part of the plan that caused it, and Detail
// describes it.
type Error struct {
	Code, Path, Detail string
	cause              error
}

// Error implements error.
func (e *Error) Error() string {
	if e.Path == "" {
		return e.Code + ": " + e.Detail
	}
	return e.Code + " at " + e.Path + ": " + e.Detail
}

// Unwrap returns the error this one was built from, when there was one.
func (e *Error) Unwrap() error { return e.cause }

// New builds an Error that stands on its own.
func New(code, path, detail string) *Error {
	return &Error{Code: code, Path: path, Detail: detail}
}

// Wrap builds an Error that errors.Is and errors.As can see through to cause.
func Wrap(code, path, detail string, cause error) *Error {
	return &Error{Code: code, Path: path, Detail: detail, cause: cause}
}
