package rasql

import (
	"fmt"

	"github.com/lestrrat-go/rasql/row"
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
	source, err := row.NewDynamic([]string{scanValueColumn}, []any{value})
	if err != nil {
		return err
	}
	return row.Assign(source, scanValueColumn, destination)
}
