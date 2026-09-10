package rasql

import "github.com/lestrrat-go/rasql/exec"

// ErrNoRows is returned by [One] when the query matched no rows. It lets a
// caller tell an absent row from a failed query, which is otherwise
// indistinguishable because both arrive as a non-nil error.
//
// It wraps [database/sql.ErrNoRows], so code that already branches on the
// standard library's sentinel keeps working without change:
//
//	user, err := rasql.One(ctx, executor, query)
//	if errors.Is(err, rasql.ErrNoRows) {
//		// no such user
//	}
//
// Only terminals that require a row report it. [All] returns an empty slice
// for an empty result, and [Rows] yields no values at all, because neither
// treats an empty result as a failure.
var ErrNoRows = exec.ErrNoRows

// ErrMultipleRows is returned by [One] when the query matched more than one
// row. It usually means the predicate is not unique, so it deserves a
// different response from ErrNoRows.
//
// They all stop reading at the second row and do not drain the result, so they
// report that more than one row matched without reporting how many.
var ErrMultipleRows = exec.ErrMultipleRows
