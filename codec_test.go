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

func TestCodecErrorsHideCodecCauseText(t *testing.T) {
	secret := errors.New("secret value should not be printed")
	err := &DecodeError{Column: "payload", Codec: "text", Err: secret}
	require.True(t, errors.Is(err, secret))
	require.False(t, strings.Contains(err.Error(), "secret value"))
	err2 := &EncodeError{Index: 2, Codec: "text", Err: secret}
	require.True(t, errors.Is(err2, secret))
	require.False(t, strings.Contains(err2.Error(), "secret value"))
}
