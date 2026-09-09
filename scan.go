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

// ScanMask records which columns of a row type a ScanDestinations call has
// already mapped, so a result set that names the same column twice is
// rejected instead of being scanned into the same field twice.
//
// Generated ScanDestinations methods build one with [NewScanMask] and call
// [ScanMask.Mark] once for each column they recognize. A hand-written
// implementation can do the same.
// The bits live in whole 64-bit words, so the allocation rounds up, but the
// column count the caller asked for is kept alongside them: the bits past the
// last column exist only because of that rounding and name no column, so Mark
// must reject them rather than accept whatever the rounding left over.
type ScanMask struct {
	words   []uint64
	columns int
}

// NewScanMask returns a mask that holds one bit per column, all unmarked.
func NewScanMask(columns int) ScanMask {
	if columns <= 0 {
		return ScanMask{}
	}
	return ScanMask{words: make([]uint64, (columns+63)/64), columns: columns}
}

// Mark records that the column at index has been mapped, and reports whether
// that column was still unmarked. A false return means the column was already
// mapped, which is how a duplicate result column is detected.
//
// Mark panics if index is negative or is at or past the column count the mask
// was built for, because either is a mistake in the calling ScanDestinations
// rather than something a result set can cause.
func (m ScanMask) Mark(index int) bool {
	if index < 0 || index >= m.columns {
		panic(fmt.Sprintf("rasql: scan mask index %d out of range for %d columns", index, m.columns))
	}
	word := index / 64
	bit := uint64(1) << (index % 64)
	if m.words[word]&bit != 0 {
		return false
	}
	m.words[word] |= bit
	return true
}
