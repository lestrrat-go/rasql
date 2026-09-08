// Package rasql provides typed SQL queries and execution for Go.
//
// Use NewExecutor to pair a database/sql handle with an engine profile. Build
// a [Query] from a generated source and projection with [Select], then execute
// it through [Rows], [All], [One], or [Maybe]. [Rows] returns a rangeable
// iter.Seq2 that yields decoded values followed by at most one execution or
// scanning error. [One] reports a row count other than one through [ErrNoRows]
// or [ErrMultipleRows].
//
// Writes use [MutationPlan] and the same [Executor]. [Native] and
// [NativeMutation] provide the explicit boundary for hand-written SQL.
//
// The schema, query, render, and dialect packages expose lower-level schema,
// expression, rendering, and engine APIs.
package rasql
