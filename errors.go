package rasql

import (
	"database/sql"
	"errors"
)

// ErrNoRows is returned by [TypedSelectBuilder.One], [QueryWriteOne],
// [QueryDeleteOne], and the Count methods when the statement matched no rows.
// It lets a caller tell an
// absent row from a
// failed query, which is otherwise indistinguishable because both arrive as a
// non-nil error.
//
// It wraps [database/sql.ErrNoRows], so code that already branches on the
// standard library's sentinel keeps working without change:
//
//	user, err := rasql.SelectFrom(users).WhereEqual(users.ID, id).One(ctx, client)
//	if errors.Is(err, rasql.ErrNoRows) {
//		// no such user
//	}
//
// Only the methods that expect one row report it. All and [QueryWriteAll]
// return an empty slice for an empty result, and Query yields no values at all,
// because none of them treats an empty result as a failure.
var ErrNoRows error = noRowsError{}

// ErrMultipleRows is returned by [TypedSelectBuilder.One], [QueryWriteOne],
// [QueryDeleteOne], and the Count methods when the statement matched more than
// one row. It usually
// means the predicate is not unique, so it deserves a different response from
// ErrNoRows.
//
// They all stop reading at the second row and do not drain the result, so they
// report that more than one row matched without reporting how many.
var ErrMultipleRows = errors.New("rasql: expected one row, got more than one")

// noRowsError gives ErrNoRows a message of its own while still unwrapping to
// database/sql.ErrNoRows. Declaring the sentinel as
// fmt.Errorf("...: %w", sql.ErrNoRows) would append the standard library's
// text to the message of every error One returns for an empty result.
type noRowsError struct{}

func (noRowsError) Error() string { return "rasql: expected one row, got none" }

// Unwrap exposes database/sql.ErrNoRows so errors.Is(err, sql.ErrNoRows) holds
// for an empty result, matching what a caller gets from sql.Row.Scan.
func (noRowsError) Unwrap() error { return sql.ErrNoRows }
