// Package dynamic reads and writes rows whose columns are known only as
// strings at run time.
//
// It is the counterpart to the root rasql package: a type you declare in Go
// stays in rasql, and a column name you only know as a string comes here. Its
// builders and execute functions take a rasql.DB, which is why the dependency
// runs one way only -- dynamic imports rasql, and rasql never imports dynamic.
package dynamic

import (
	"database/sql"
	"iter"

	"github.com/lestrrat-go/rasql/internal/rowvalue"
)

// Row contains one database result row whose columns are known only at run
// time. Every row of one query shares the same columnHeader, so decoding a row
// costs one slice of values plus a lookup through the header's index, rather
// than a map[string]any allocated fresh for every row.
type Row = rowvalue.Row

// NewRow validates column names and values and returns an independent row value.
func NewRow(names []string, values []any) (Row, error) {
	return rowvalue.NewRow(names, values)
}

// Get decodes the named value in r as T.
func Get[T any](r Row, name string) (T, error) {
	return rowvalue.Get[T](r, name)
}

// Assign decodes the named value in r into destination.
func Assign[T any](r Row, name string, destination *T) error {
	return rowvalue.Assign(r, name, destination)
}

// Decode populates T from rasql-tagged fields and snake-cased exported field
// names. It is the only read mapping; a row type that carries generated scan
// methods is filled by those instead, through the typed builders rather than
// through Decode.
//
// The per-type facts this needs -- which fields map to which columns -- are
// resolved once per type by planFor and cached, rather than recomputed on
// every call. A type whose plan holds an error (a bad tag or an unexported
// tagged field) reports the same error on every call, resolved before any row
// is read: an error found while planning always wins over a per-row error,
// such as a missing column, that a field reached earlier in declaration order
// would have reported first under the old, per-row walk.
func Decode[T any](r Row) (T, error) {
	return rowvalue.Decode[T](r)
}

// Scan returns a rangeable sequence of the result rows in rows.
//
// Scan takes ownership of rows and closes them when the sequence ends, whether
// iteration reached the last row, stopped early through break or return, or
// stopped on an error. A caller that takes a *sql.Rows from an executor and does
// not hand it to Scan closes it itself.
//
// A nil rows yields nothing and closes nothing. A debug queryer that logs a
// statement instead of running it returns (nil, nil), and this is what lets its
// result travel the same path as a real one.
//
// The sequence is single-use. Ranging over it a second time yields nothing,
// because the underlying rows are already closed.
func Scan(rows *sql.Rows) iter.Seq2[Row, error] {
	return rowvalue.Scan(rows)
}
