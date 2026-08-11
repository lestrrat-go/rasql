// Package rasql provides typed SQL queries and execution for Go.
//
// Use New to pair a database/sql handle with a SQL dialect. The DB it returns
// is the only handle type in this package: DB.Begin starts a transaction and
// returns another DB, so a transaction is the same type as the database it was
// started on, and every builder and function that takes one takes either.
// Generated table descriptors use MustTableDef, then SelectFrom, Insert,
// Update, DeleteFrom, and CreateTable execute typed database operations: the
// builders take the DB at their terminal call, so one builder runs inside a
// transaction and outside it alike.
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
