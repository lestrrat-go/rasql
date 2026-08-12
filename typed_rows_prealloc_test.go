package rasql

import (
	"math"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

// preallocNarrowRow is a few bytes wide, representative of a typical
// generated row type.
type preallocNarrowRow struct {
	ID int32
}

// preallocWideRow carries a multi-kilobyte array field so its element size
// alone approaches, and for a large hint exceeds, maxCollectPreallocBytes.
type preallocWideRow struct {
	Payload [4096]byte
}

// preallocEmptyRow has zero size, which exercises the size < 1 guard in
// preallocCapacity.
type preallocEmptyRow struct{}

// TestPreallocCapacity is a table-driven test over preallocCapacity, table-driven
// across both the hint and the row type so the byte-budget clamp is checked
// against a narrow type, a deliberately wide type, and a zero-sized type.
// math.MaxInt against preallocNarrowRow (4 bytes, but the size < 1 guard makes
// the smallest possible element 1 byte for this check) is the case a naive
// hint * size would overflow.
func TestPreallocCapacity(t *testing.T) {
	hints := []int{-1, 0, 1, 8, math.MaxInt}

	t.Run("narrow row", func(t *testing.T) {
		size := int(reflect.TypeFor[preallocNarrowRow]().Size())
		for _, hint := range hints {
			got := preallocCapacity[preallocNarrowRow](hint)
			checkPreallocCapacity(t, hint, size, got)
		}
	})

	t.Run("wide row", func(t *testing.T) {
		size := int(reflect.TypeFor[preallocWideRow]().Size())
		for _, hint := range hints {
			got := preallocCapacity[preallocWideRow](hint)
			checkPreallocCapacity(t, hint, size, got)
		}
	})

	t.Run("empty row", func(t *testing.T) {
		size := int(reflect.TypeFor[preallocEmptyRow]().Size())
		for _, hint := range hints {
			got := preallocCapacity[preallocEmptyRow](hint)
			checkPreallocCapacity(t, hint, size, got)
		}
	})
}

// checkPreallocCapacity asserts the invariants preallocCapacity must hold for
// any hint and any element size: the result is never negative, never exceeds
// the hint, and never reserves more than maxCollectPreallocBytes.
func checkPreallocCapacity(t *testing.T, hint, size, got int) {
	t.Helper()

	require.GreaterOrEqual(t, got, 0, "hint=%d size=%d", hint, size)
	if hint <= 0 {
		require.Zero(t, got, "hint=%d size=%d", hint, size)
	} else {
		require.LessOrEqual(t, got, hint, "hint=%d size=%d", hint, size)
	}

	budgetSize := size
	if budgetSize < 1 {
		budgetSize = 1
	}
	require.LessOrEqual(t, got*budgetSize, maxCollectPreallocBytes, "hint=%d size=%d", hint, size)
}
