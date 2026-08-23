package exec

import (
	"database/sql"
	"errors"
)

// ErrNoRows is the sentinel every single-row read in rasql and rasql/dynamic
// reports when a statement matched no rows. It lets a caller tell an absent
// row from a failed query, which is otherwise indistinguishable because both
// arrive as a non-nil error.
//
// It wraps [database/sql.ErrNoRows], so code that already branches on the
// standard library's sentinel keeps working without change.
var ErrNoRows error = noRowsError{}

// ErrMultipleRows is the sentinel every single-row read in rasql and
// rasql/dynamic reports when a statement matched more than one row. A reader
// expecting one row stops at the second rather than counting, so it reports
// that more than one row matched without reporting how many.
var ErrMultipleRows = errors.New("rasql: expected one row, got more than one")

// noRowsError gives ErrNoRows a message of its own while still unwrapping to
// database/sql.ErrNoRows. Declaring the sentinel as
// fmt.Errorf("...: %w", sql.ErrNoRows) would append the standard library's
// text to the message of every error a single-row read returns for an empty
// result.
type noRowsError struct{}

func (noRowsError) Error() string { return "rasql: expected one row, got none" }

// Unwrap exposes database/sql.ErrNoRows so errors.Is(err, sql.ErrNoRows) holds
// for an empty result, matching what a caller gets from sql.Row.Scan.
func (noRowsError) Unwrap() error { return sql.ErrNoRows }
