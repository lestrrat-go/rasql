package rasql

import (
	"database/sql/driver"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFrameGraphValuePreservesDriverTypes(t *testing.T) {
	stringFrame, err := frameGraphValue(driver.Value("1"))
	require.NoError(t, err)
	bytesFrame, err := frameGraphValue(driver.Value([]byte("1")))
	require.NoError(t, err)
	require.NotEqual(t, stringFrame, bytesFrame)

	first, err := frameGraphValue(driver.Value(time.Date(2025, 1, 1, 10, 2, 3, 4, time.FixedZone("x", 9*60*60))))
	require.NoError(t, err)
	second, err := frameGraphValue(driver.Value(time.Date(2025, 1, 1, 1, 2, 3, 4, time.UTC)))
	require.NoError(t, err)
	require.Equal(t, first, second)
}

func TestFrameGraphValueRejectsNaN(t *testing.T) {
	_, err := frameGraphValue(driver.Value(math.NaN()))
	require.Error(t, err)
}
