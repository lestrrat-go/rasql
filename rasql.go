// Package rasql provides typed SQL queries and execution for Go.
//
// Use New to pair a database/sql handle with a SQL dialect. Generated table
// descriptors use MustTable, then SelectFrom, Insert, Update, DeleteFrom, and
// Create execute typed database operations.
// Query operations return a rangeable iter.Seq2 sequence plus any construction
// error. The sequence yields rows followed by at most one execution or scanning
// error. [TypedSelectBuilder.One] reports a row count other than one through
// [ErrNoRows] or [ErrMultipleRows].
//
// The schema, query, render, row, and dialect packages expose lower-level APIs
// for schema generation, dynamic queries, rendering, and result handling.
package rasql
