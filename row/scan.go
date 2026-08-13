package row

import (
	"database/sql"
	"iter"

	"github.com/lestrrat-go/rasql/internal/rowvalue"
)

// ScanSource provides database/sql's Scan operation. Both [sql.Row] and
// [sql.Rows] implement it.
type ScanSource interface {
	Scan(...any) error
}

// Scanner is implemented by row types that scan a statically-known result
// projection directly into their fields.
//
// Generated row types implement Scanner. Typed queries use it when their
// builder owns the complete table projection.
type Scanner interface {
	ScanRow(ScanSource) error
}

// DestinationScanner is implemented by row types that map result-column names
// to destinations for [ScanSource.Scan]. Generated row types implement it so
// typed queries can scan partial or reordered result sets without Dynamic.
//
// A row type that implements DestinationScanner alone maps whichever columns it
// chooses to. One that implements Scanner as well states that it maps a whole
// table, which is the pair rasqlgen writes, and a typed write reading a
// RETURNING clause into such a row type requires the clause to project every
// column of the table it writes.
type DestinationScanner interface {
	ScanDestinations([]string) ([]any, error)
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
func Scan(rows *sql.Rows) iter.Seq2[Dynamic, error] {
	return rowvalue.Scan(rows)
}
