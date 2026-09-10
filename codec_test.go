package rasql

import (
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type testCodec struct{}

func (testCodec) Encode(value any) (driver.Value, error) { return value, nil }
func (testCodec) Decode(source any, destination any) error {
	*destination.(*string) = source.(string)
	return nil
}

func TestCodecRegistryCopiesValuesAndRejectsInvalidIDs(t *testing.T) {
	registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"text": testCodec{}})
	require.NoError(t, err)
	codec, ok := registry.Lookup("text")
	require.True(t, ok)
	require.NotNil(t, codec)
	_, err = NewCodecRegistry(map[CodecID]ValueCodec{"": testCodec{}})
	require.Error(t, err)
	_, err = NewCodecRegistry(map[CodecID]ValueCodec{"nil": (*testCodec)(nil)})
	require.Error(t, err)
}

// A column that no codec decoded failed inside rasql's own conversion, so its
// message names the cause. TestCodecErrorsHideCodecCauseText covers the other
// half of the same rule, where a codec produced the error and its text stays
// out of the message.
func TestDecodeErrorNamesItsCauseWithoutACodec(t *testing.T) {
	cause := errors.New(`expected int64, got []uint8`)
	err := &DecodeError{Column: "total", Err: cause}
	require.True(t, errors.Is(err, cause))
	require.Equal(t, `decode column "total" failed: expected int64, got []uint8`, err.Error())
}

func TestCodecErrorsHideCodecCauseText(t *testing.T) {
	secret := errors.New("secret value should not be printed")
	err := &DecodeError{Column: "payload", Codec: "text", Err: secret}
	require.True(t, errors.Is(err, secret))
	require.False(t, strings.Contains(err.Error(), "secret value"))
	err2 := &EncodeError{Index: 2, Codec: "text", Err: secret}
	require.True(t, errors.Is(err2, secret))
	require.False(t, strings.Contains(err2.Error(), "secret value"))
}
