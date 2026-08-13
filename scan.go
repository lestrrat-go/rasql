package rasql

import (
	"fmt"

	"github.com/lestrrat-go/rasql/internal/rowvalue"
)

// scanValueColumn is the name ScanValue hands the decoder, which looks a value
// up by column. It names no real column and is an artifact of routing one value
// through the row-shaped API; step 3 replaces that lookup with a direct call.
const scanValueColumn = "value"

// ScanValue decodes one driver value into destination, applying the same
// conversions a mapped struct field gets: a destination implementing
// sql.Scanner is asked first, a time.Time destination takes a time.Time or
// parses the driver's text, and the numeric, string, and byte-slice
// conversions follow. Generated row types call it from the time scanner they
// emit, and a hand-written ScanDestinations can call it for a destination that
// needs the same treatment.
func ScanValue[T any](destination *T, value any) error {
	if destination == nil {
		return fmt.Errorf("rasql: scan destination must not be nil")
	}
	source, err := rowvalue.NewRow([]string{scanValueColumn}, []any{value})
	if err != nil {
		return err
	}
	return rowvalue.Assign(source, scanValueColumn, destination)
}

// ScanSource provides database/sql's Scan operation. Both [database/sql.Row]
// and [database/sql.Rows] implement it, which is what lets one row type scan
// from either.
type ScanSource interface {
	Scan(...any) error
}

// Scanner is implemented by row types that scan a statically-known result
// projection directly into their fields.
//
// This is not [database/sql.Scanner], despite the shared name, and one says
// nothing about the other. A database/sql.Scanner converts a single column
// value into a single destination, and rasql calls it during that conversion
// like any other consumer of database/sql. A rasql Scanner reads a whole row:
// it receives the result set itself as a [ScanSource] and fills every field of
// the row type, in the order the projection states. A row type may usefully
// implement both. [ScanValue] is rasql's own single-value operation.
//
// Generated row types implement Scanner. Typed queries use it when the builder
// owns the complete table projection, so the column order is known before the
// query runs; when it is not, they use [DestinationScanner] instead.
type Scanner interface {
	ScanRow(ScanSource) error
}

// DestinationScanner is implemented by row types that map result-column names
// to destinations for [ScanSource.Scan]. Generated row types implement it so
// typed queries can scan a partial or reordered result set without falling
// back to the reflective decode path.
//
// A row type that implements DestinationScanner alone maps whichever columns it
// chooses to. One that implements [Scanner] as well states that it maps a whole
// table, which is the pair rasqlgen writes, and a typed write reading a
// RETURNING clause into such a row type requires the clause to project every
// column of the table it writes.
type DestinationScanner interface {
	ScanDestinations([]string) ([]any, error)
}
