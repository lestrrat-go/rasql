package cursorcodec_test

import (
	"encoding/binary"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql/internal/cursorcodec"
	"github.com/stretchr/testify/require"
)

type namedBool bool
type namedString string
type namedInt8 int8
type namedUint8 uint8

func TestValue(t *testing.T) {
	t.Run("builtin values preserve named types and reject overflow", func(t *testing.T) {
		encoded, err := cursorcodec.EncodeValue(namedBool(true))
		require.NoError(t, err)
		decoded, err := cursorcodec.DecodeValue(encoded, reflect.TypeOf(namedBool(false)))
		require.NoError(t, err)
		require.IsType(t, namedBool(false), decoded)

		encoded, err = cursorcodec.EncodeValue(namedString("value"))
		require.NoError(t, err)
		decoded, err = cursorcodec.DecodeValue(encoded, reflect.TypeOf(namedString("")))
		require.NoError(t, err)
		require.IsType(t, namedString(""), decoded)

		intBytes, err := cursorcodec.EncodeValue(int64(128))
		require.NoError(t, err)
		_, err = cursorcodec.DecodeValue(intBytes, reflect.TypeOf(namedInt8(0)))
		require.Error(t, err)

		uintBytes, err := cursorcodec.EncodeValue(uint64(256))
		require.NoError(t, err)
		_, err = cursorcodec.DecodeValue(uintBytes, reflect.TypeOf(namedUint8(0)))
		require.Error(t, err)

		_, err = cursorcodec.EncodeValue(math.NaN())
		require.Error(t, err)

		for _, value := range []float64{math.Inf(1), -math.Inf(1)} {
			encoded, encodeErr := cursorcodec.EncodeValue(value)
			require.NoError(t, encodeErr)
			decoded, decodeErr := cursorcodec.DecodeValue(encoded, reflect.TypeOf(float32(0)))
			require.NoError(t, decodeErr)
			require.Equal(t, float32(value), decoded)
		}

		// A float whose bits decode to NaN never comes from EncodeValue, so the
		// decoder has to reject it rather than hand back a value that cannot be
		// compared against anything.
		malformedNaN := make([]byte, 8)
		binary.BigEndian.PutUint64(malformedNaN, math.Float64bits(math.NaN())^(1<<63))
		_, err = cursorcodec.DecodeValue(malformedNaN, reflect.TypeOf(float64(0)))
		require.Error(t, err)

		encoded, err = cursorcodec.EncodeValue(1e40)
		require.NoError(t, err)
		_, err = cursorcodec.DecodeValue(encoded, reflect.TypeOf(float32(0)))
		require.Error(t, err)
	})

	t.Run("builtin time round-trips the full database range", func(t *testing.T) {
		for _, want := range []time.Time{
			time.Date(1, time.January, 1, 0, 0, 0, 123456789, time.FixedZone("offset", 9*60*60)),
			time.Date(9999, time.December, 31, 23, 59, 59, 987654321, time.FixedZone("offset", -8*60*60)),
			time.Now().Add(123456789 * time.Nanosecond),
		} {
			encoded, err := cursorcodec.EncodeValue(want)
			require.NoError(t, err)
			require.Len(t, encoded, 12)
			got, err := cursorcodec.DecodeValue(encoded, reflect.TypeOf(time.Time{}))
			require.NoError(t, err)
			require.Equal(t, want.UTC(), got)
		}
	})

	t.Run("rejects values and types the format cannot carry", func(t *testing.T) {
		_, err := cursorcodec.EncodeValue(nil)
		require.Error(t, err)
		_, err = cursorcodec.EncodeValue(struct{ Field int }{})
		require.Error(t, err)
		_, err = cursorcodec.DecodeValue([]byte{1}, reflect.TypeOf(struct{ Field int }{}))
		require.Error(t, err)
	})
}
