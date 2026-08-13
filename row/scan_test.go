package row_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/row"
	"github.com/stretchr/testify/require"
)

func TestScanMask(t *testing.T) {
	t.Run("marks each column once", func(t *testing.T) {
		mask := row.NewScanMask(3)
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
		mask := row.NewScanMask(130)
		require.Len(t, mask, 3)
		for _, index := range []int{63, 64, 65, 129} {
			require.True(t, mask.Mark(index), "column %d starts unmarked", index)
		}
		for _, index := range []int{63, 64, 65, 129} {
			require.False(t, mask.Mark(index), "column %d stays marked", index)
		}
		require.True(t, mask.Mark(0))
		require.True(t, mask.Mark(128))
	})

	t.Run("sizes to whole words", func(t *testing.T) {
		require.Len(t, row.NewScanMask(1), 1)
		require.Len(t, row.NewScanMask(64), 1)
		require.Len(t, row.NewScanMask(65), 2)
		require.Empty(t, row.NewScanMask(0))
	})

	// An index the mask was not sized for is a mistake in the calling
	// ScanDestinations, so it panics rather than reporting a duplicate column
	// that the result set never contained.
	t.Run("panics outside the sized range", func(t *testing.T) {
		mask := row.NewScanMask(2)
		require.PanicsWithValue(t, "row: scan mask index 64 out of range for 64 columns", func() {
			mask.Mark(64)
		})
		require.PanicsWithValue(t, "row: scan mask index -1 out of range for 64 columns", func() {
			mask.Mark(-1)
		})
		require.Panics(t, func() {
			row.NewScanMask(0).Mark(0)
		})
	})
}
