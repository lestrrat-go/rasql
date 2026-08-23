package rasql

import "github.com/lestrrat-go/rasql/exec"

// ErrNoRows is returned by [TypedSelectBuilder.One], [QueryWriteOne],
// [QueryDeleteOne], and the Count methods when the statement matched no rows.
// rasql/dynamic's own single-row terminal, SelectBuilder.Count, reports this
// same value rather than a sentinel of its own.
// It lets a caller tell an
// absent row from a
// failed query, which is otherwise indistinguishable because both arrive as a
// non-nil error.
//
// It wraps [database/sql.ErrNoRows], so code that already branches on the
// standard library's sentinel keeps working without change:
//
//	user, err := rasql.SelectFrom(users).WhereEqual(users.ID(), id).One(ctx, client)
//	if errors.Is(err, rasql.ErrNoRows) {
//		// no such user
//	}
//
// Only the methods that expect one row report it. All and [QueryWriteAll]
// return an empty slice for an empty result, and Query yields no values at all,
// because none of them treats an empty result as a failure.
var ErrNoRows = exec.ErrNoRows

// ErrMultipleRows is returned by [TypedSelectBuilder.One], [QueryWriteOne],
// [QueryDeleteOne], and the Count methods when the statement matched more than
// one row. rasql/dynamic's own single-row terminal, SelectBuilder.Count,
// reports this same value rather than a sentinel of its own.
// It usually
// means the predicate is not unique, so it deserves a different response from
// ErrNoRows.
//
// They all stop reading at the second row and do not drain the result, so they
// report that more than one row matched without reporting how many.
var ErrMultipleRows = exec.ErrMultipleRows
