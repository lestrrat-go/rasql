package row

import (
	"database/sql"
	"fmt"
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

// ScanMask records which columns of a row type a [DestinationScanner.ScanDestinations]
// call has already mapped, so a result set that names the same column twice is
// rejected instead of being scanned into the same field twice.
//
// Generated ScanDestinations methods build one with [NewScanMask] and call
// [ScanMask.Mark] once for each column they recognize. A hand-written
// implementation can do the same.
type ScanMask []uint64

// NewScanMask returns a mask that holds one bit per column, all unmarked.
func NewScanMask(columns int) ScanMask {
	if columns <= 0 {
		return nil
	}
	return make(ScanMask, (columns+63)/64)
}

// Mark records that the column at index has been mapped, and reports whether
// that column was still unmarked. A false return means the column was already
// mapped, which is how a duplicate result column is detected.
//
// Mark panics if index is negative or names a column the mask was not sized
// for, because either is a mistake in the calling ScanDestinations rather than
// something a result set can cause.
func (m ScanMask) Mark(index int) bool {
	if index < 0 || index >= len(m)*64 {
		panic(fmt.Sprintf("row: scan mask index %d out of range for %d columns", index, len(m)*64))
	}
	word := index / 64
	bit := uint64(1) << (index % 64)
	if m[word]&bit != 0 {
		return false
	}
	m[word] |= bit
	return true
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
