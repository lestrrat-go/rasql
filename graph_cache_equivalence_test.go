package rasql

import (
	"database/sql/driver"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGraphCacheCanonicalEncodedValues(t *testing.T) {
	first, err := normalizeGraphValue(float32(1.25))
	require.NoError(t, err)
	second, err := normalizeGraphValue(float64(1.25))
	require.NoError(t, err)
	firstFrame, err := frameGraphValue(first)
	require.NoError(t, err)
	secondFrame, err := frameGraphValue(second)
	require.NoError(t, err)
	require.Equal(t, firstFrame, secondFrame)

	stamp := time.Date(2026, 9, 7, 1, 2, 3, 4, time.UTC)
	stampFrame, err := frameGraphValue(driver.Value(stamp))
	require.NoError(t, err)
	require.NotEmpty(t, stampFrame)
	_, err = frameGraphValue(driver.Value(math.NaN()))
	require.Error(t, err)
}

func TestGraphCacheDecoderCompatibilityIncludesEmptyEntries(t *testing.T) {
	one := struct{ Name string }{Name: "one"}
	two := struct{ Name string }{Name: "two"}
	entry := graphCacheEntry{decoder: one}
	require.True(t, reflect.DeepEqual(entry.decoder, one))
	require.False(t, reflect.DeepEqual(entry.decoder, two))
}

func TestGraphCacheSnapshotCopiesBytes(t *testing.T) {
	value := []byte("snapshot")
	copyValue := graphCloneEncoded(value).([]byte)
	value[0] = 'X'
	require.Equal(t, []byte("snapshot"), copyValue)
	copyValue[0] = 'Y'
	require.Equal(t, byte('X'), value[0])
}
