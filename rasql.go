// Package rasql provides typed SQL queries and execution for Go.
//
// Use New to pair a database/sql handle with a SQL dialect, or Begin to pair a
// new transaction with one. Both produce an Executor. Generated table
// descriptors use MustTableDef, then SelectFrom, Insert, Update, DeleteFrom, and
// CreateTable execute typed database operations: the builders take the Executor at
// their terminal call, so one builder runs against a Client and a Tx alike.
// Query operations return a rangeable iter.Seq2 sequence plus any construction
// error. The sequence yields rows followed by at most one scanning error.
// [TypedSelectBuilder.One] and [QueryWriteOne] report a row count other than one
// through [ErrNoRows] or [ErrMultipleRows].
//
// A write statement built through the query package that carries a RETURNING
// clause is read with QueryWrite, or the typed QueryWriteAll and QueryWriteOne,
// instead of Exec, which rejects it.
//
// The schema, query, render, row, and dialect packages expose lower-level APIs
// for schema generation, dynamic queries, rendering, and result handling.
package rasql
