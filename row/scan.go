package row

import (
	"database/sql"
	"fmt"
	"iter"
)

// ScanSource provides database/sql's Scan operation. Both [sql.Row] and
// [sql.Rows] implement it.
type ScanSource interface {
	Scan(...any) error
}

// Scanner is implemented by row types that scan matching result columns
// directly into their fields. ScanColumns returns the expected columns in the
// order ScanRow passes their destinations to source.
//
// Generated row types implement Scanner. Typed queries use it when the result
// columns exactly match ScanColumns.
type Scanner interface {
	ScanColumns() []string
	ScanRow(ScanSource) error
}

// DestinationScanner is implemented by row types that map result-column names
// to destinations for [ScanSource.Scan]. Generated row types implement it so
// typed queries can scan partial or reordered result sets without Dynamic.
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
	return func(yield func(Dynamic, error) bool) {
		if rows == nil {
			return
		}
		defer func() { _ = rows.Close() }()

		names, err := rows.Columns()
		if err != nil {
			yield(Dynamic{}, fmt.Errorf("row: read result columns: %w", err))
			return
		}
		for rows.Next() {
			values := make([]any, len(names))
			destinations := make([]any, len(values))
			for index := range values {
				destinations[index] = &values[index]
			}
			if err := rows.Scan(destinations...); err != nil {
				yield(Dynamic{}, fmt.Errorf("row: scan result row: %w", err))
				return
			}
			decoded, err := New(names, values)
			if err != nil {
				yield(Dynamic{}, fmt.Errorf("row: create result row: %w", err))
				return
			}
			if !yield(decoded, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Dynamic{}, fmt.Errorf("row: iterate result rows: %w", err))
		}
	}
}
