package graphkey_test

import (
	"database/sql/driver"
	"math"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql/internal/graphkey"
	"github.com/stretchr/testify/require"
)

func TestFrame(t *testing.T) {
	t.Run("preserves driver types", func(t *testing.T) {
		stringFrame, err := graphkey.Frame(driver.Value("1"))
		require.NoError(t, err)
		bytesFrame, err := graphkey.Frame(driver.Value([]byte("1")))
		require.NoError(t, err)
		require.NotEqual(t, stringFrame, bytesFrame)

		first, err := graphkey.Frame(driver.Value(time.Date(2025, 1, 1, 10, 2, 3, 4, time.FixedZone("x", 9*60*60))))
		require.NoError(t, err)
		second, err := graphkey.Frame(driver.Value(time.Date(2025, 1, 1, 1, 2, 3, 4, time.UTC)))
		require.NoError(t, err)
		require.Equal(t, first, second)
	})

	t.Run("rejects NaN", func(t *testing.T) {
		_, err := graphkey.Frame(driver.Value(math.NaN()))
		require.Error(t, err)
	})

	// A fingerprint identifies a plan rather than matching rows, so NaN is
	// framed there instead of refused.
	t.Run("frames NaN for a fingerprint", func(t *testing.T) {
		framed, err := graphkey.FrameAllowingNaN(driver.Value(math.NaN()))
		require.NoError(t, err)
		require.NotEmpty(t, framed)
	})

	t.Run("rejects a value the format cannot carry", func(t *testing.T) {
		_, err := graphkey.Frame(driver.Value(struct{ Field int }{}))
		require.Error(t, err)
	})
}

func TestNormalize(t *testing.T) {
	t.Run("converts through the driver contract", func(t *testing.T) {
		value, err := graphkey.Normalize(int32(7))
		require.NoError(t, err)
		require.Equal(t, int64(7), value)

		value, err = graphkey.Normalize(nil)
		require.NoError(t, err)
		require.Nil(t, value)
	})

	t.Run("rejects a value no driver accepts", func(t *testing.T) {
		_, err := graphkey.Normalize(struct{ Field int }{})
		require.Error(t, err)
	})
}

func TestValidateDriverValue(t *testing.T) {
	require.NoError(t, graphkey.ValidateDriverValue(nil))
	require.NoError(t, graphkey.ValidateDriverValue(int64(1)))
	require.Error(t, graphkey.ValidateDriverValue(driver.Value(int32(1))))
}
