package rowvalue

import (
	"fmt"
	"iter"
)

type rowSource interface {
	Columns() ([]string, error)
	Next() bool
	Scan(...any) error
	Close() error
	Err() error
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
// result travel the same path as a real one. A non-nil interface value holding a
// nil pointer is not a nil rows: Scan calls Columns on it, and a *sql.Rows
// dereferences nil there.
//
// The sequence is single-use. Ranging over it a second time yields nothing,
// because the underlying rows are already closed.
func Scan(rows rowSource) iter.Seq2[Row, error] {
	return scanSource(rows, true)
}

// ScanSource decodes rows and optionally leaves ownership with the caller.
//
// A nil rows yields nothing and closes nothing, the same as Scan.
func ScanSource(rows rowSource, closeRows bool) iter.Seq2[Row, error] {
	return scanSource(rows, closeRows)
}

func scanSource(rows rowSource, closeRows bool) iter.Seq2[Row, error] {
	return func(yield func(Row, error) bool) {
		if rows == nil {
			return
		}
		if closeRows {
			defer func() { _ = rows.Close() }()
		}

		names, err := rows.Columns()
		if err != nil {
			yield(Row{}, fmt.Errorf("row: read result columns: %w", err))
			return
		}

		// Reused across every row rather than allocated per row: rows.Scan
		// writes through the *any destinations, which rebinds values[i] to a
		// new interface value on each call and never mutates the object the
		// old one pointed at, so reusing the buffers is safe. See newRowFrom
		// for why the per-row Row still needs its own copy.
		values := make([]any, len(names))
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}

		// Built on the first row only, from that row's names. The duplicate
		// and empty-name checks used to run inside NewRow on every row,
		// so a query whose result has duplicate column names but returns zero
		// rows yielded nothing rather than an error; building the header
		// before the loop would change that.
		var columns *columnHeader
		for rows.Next() {
			if err := rows.Scan(destinations...); err != nil {
				yield(Row{}, fmt.Errorf("row: scan result row: %w", err))
				return
			}
			if columns == nil {
				columns, err = newColumnHeader(names)
				if err != nil {
					yield(Row{}, fmt.Errorf("row: create result row: %w", err))
					return
				}
			}
			decoded := newRowFrom(columns, values)
			if !yield(decoded, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Row{}, fmt.Errorf("row: iterate result rows: %w", err))
		}
	}
}

// newRowFrom builds one row's Row from the query's shared columns header
// and the current contents of the reused values buffer.
//
// It differs from NewRow in two ways.
//
// First, it takes an already-built *columnHeader instead of building one from
// names: the header, including its cloned copy of names, is built once per
// query by the caller rather than once per row. sql.Rows.Columns returns the
// driver's own slice without copying it (database/sql/sql.go, (*Rows).Columns
// simply returns rs.rowsi.Columns()), and a Row retains that slice through
// its header, so the header must hold a copy the driver cannot reach; cloning
// it once per query is enough.
//
// Second, it copies values into a fresh per-row slice but does not clone each
// element the way NewRow's cloneValue does for a []byte source. The values
// buffer is filled by rows.Scan through *any destinations, and for a []byte
// source database/sql's convertAssign already does *d = bytes.Clone(s) on the
// way into an *any destination (database/sql/convert.go, the "case []byte:" /
// "case *any:" arm), so the slice a value holds here is already private to
// this row. NewRow keeps cloneValue because its caller's values carry no
// such guarantee.
func newRowFrom(columns *columnHeader, values []any) Row {
	cloned := make([]any, len(values))
	copy(cloned, values)
	return Row{columns: columns, values: cloned}
}
