package rasql_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql"
	"github.com/stretchr/testify/require"
)

var (
	_ rasql.ScanSource = (*sql.Row)(nil)
	_ rasql.ScanSource = (*sql.Rows)(nil)
)

// scanValueRecorder implements sql.Scanner. Its underlying type is a struct
// that no built-in conversion in assign handles, so a successful ScanValue
// call against it can only have gone through Scan, proving sql.Scanner is
// asked before any built-in conversion is tried.
type scanValueRecorder struct {
	value any
}

func (r *scanValueRecorder) Scan(value any) error {
	r.value = value
	return nil
}

func TestScanValue(t *testing.T) {
	t.Run("time.Time from a time.Time value", func(t *testing.T) {
		want := time.Date(2026, time.August, 1, 12, 30, 0, 0, time.UTC)
		var destination time.Time
		require.NoError(t, rasql.ScanValue(&destination, want))
		require.Equal(t, want, destination)
	})

	t.Run("time.Time from driver text", func(t *testing.T) {
		var destination time.Time
		require.NoError(t, rasql.ScanValue(&destination, "2026-08-01 12:30:00"))
		require.Equal(t, time.Date(2026, time.August, 1, 12, 30, 0, 0, time.UTC), destination)
	})

	t.Run("time.Time from driver bytes", func(t *testing.T) {
		var destination time.Time
		require.NoError(t, rasql.ScanValue(&destination, []byte("2026-08-01 12:30:00")))
		require.Equal(t, time.Date(2026, time.August, 1, 12, 30, 0, 0, time.UTC), destination)
	})

	t.Run("*time.Time destination takes a value", func(t *testing.T) {
		want := time.Date(2026, time.August, 1, 12, 30, 0, 0, time.UTC)
		var destination *time.Time
		require.NoError(t, rasql.ScanValue(&destination, want))
		require.NotNil(t, destination)
		require.Equal(t, want, *destination)
	})

	t.Run("*time.Time destination takes nil", func(t *testing.T) {
		existing := time.Now()
		destination := &existing
		require.NoError(t, rasql.ScanValue(&destination, nil))
		require.Nil(t, destination)
	})

	t.Run("int64 destination from a uint64 driver value", func(t *testing.T) {
		var destination int64
		require.NoError(t, rasql.ScanValue(&destination, uint64(42)))
		require.Equal(t, int64(42), destination)
	})

	t.Run("int64 destination rejects an out-of-range uint64", func(t *testing.T) {
		var destination int64
		err := rasql.ScanValue(&destination, uint64(18446744073709551615))
		require.ErrorContains(t, err, "18446744073709551615 overflows int64")
	})

	t.Run("sql.Scanner is asked before any built-in conversion", func(t *testing.T) {
		var destination scanValueRecorder
		require.NoError(t, rasql.ScanValue(&destination, int64(42)))
		require.Equal(t, int64(42), destination.value)
	})

	t.Run("nil destination", func(t *testing.T) {
		var destination *int64
		err := rasql.ScanValue(destination, int64(1))
		require.ErrorContains(t, err, "rasql: scan destination must not be nil")
	})

	t.Run("type mismatch", func(t *testing.T) {
		var destination bool
		err := rasql.ScanValue(&destination, "not a bool")
		require.Error(t, err)
	})
}

func TestScanMask(t *testing.T) {
	t.Run("marks each column once", func(t *testing.T) {
		mask := rasql.NewScanMask(3)
		require.True(t, mask.Mark(0))
		require.True(t, mask.Mark(2))
		require.False(t, mask.Mark(0), "a second Mark of the same column reports the duplicate")
		require.False(t, mask.Mark(2))
		require.True(t, mask.Mark(1), "an untouched column is unaffected by its neighbors")
	})

	// A table wider than one 64-bit word is the case the mask exists to hide
	// from generated code, so the columns on either side of the word boundary
	// must stay independent.
	t.Run("spans word boundaries", func(t *testing.T) {
		mask := rasql.NewScanMask(130)
		for _, index := range []int{63, 64, 65, 129} {
			require.True(t, mask.Mark(index), "column %d starts unmarked", index)
		}
		for _, index := range []int{63, 64, 65, 129} {
			require.False(t, mask.Mark(index), "column %d stays marked", index)
		}
		require.True(t, mask.Mark(0))
		require.True(t, mask.Mark(128))
	})

	// The bits are packed into whole 64-bit words, so a column count that is
	// not a multiple of 64 leaves spare bits in the last word. Those bits name
	// no column, and the range Mark accepts is the requested column count
	// rather than whatever the rounding allocated.
	t.Run("accepts exactly the requested column count", func(t *testing.T) {
		require.True(t, rasql.NewScanMask(1).Mark(0))
		require.True(t, rasql.NewScanMask(64).Mark(63))
		require.True(t, rasql.NewScanMask(65).Mark(64), "the first column past a word boundary is still a column")
		require.Panics(t, func() {
			rasql.NewScanMask(65).Mark(65)
		})
	})

	// An index the mask was not sized for is a mistake in the calling
	// ScanDestinations, so it panics rather than reporting a duplicate column
	// that the result set never contained.
	t.Run("panics outside the requested range", func(t *testing.T) {
		mask := rasql.NewScanMask(2)
		// Index 2 is inside the mask's single allocated word but outside the
		// two columns it was built for.
		require.PanicsWithValue(t, "rasql: scan mask index 2 out of range for 2 columns", func() {
			mask.Mark(2)
		})
		require.PanicsWithValue(t, "rasql: scan mask index 63 out of range for 2 columns", func() {
			mask.Mark(63)
		})
		require.PanicsWithValue(t, "rasql: scan mask index 64 out of range for 2 columns", func() {
			mask.Mark(64)
		})
		require.PanicsWithValue(t, "rasql: scan mask index -1 out of range for 2 columns", func() {
			mask.Mark(-1)
		})
		require.PanicsWithValue(t, "rasql: scan mask index 0 out of range for 0 columns", func() {
			rasql.NewScanMask(0).Mark(0)
		})
	})
}
