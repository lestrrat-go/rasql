// Package rasql provides typed SQL queries and execution for Go.
//
// Use New to pair a database/sql handle with a SQL dialect. Generated table
// descriptors use MustTable, then SelectFrom, Insert, Update, and Create
// execute typed database operations. Importing rasql registers the pure-Go
// SQLite driver as "sqlite".
//
// The schema, query, render, row, and dialect packages expose lower-level APIs
// for schema generation, dynamic queries, rendering, and result handling.
package rasql
