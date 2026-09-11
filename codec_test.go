package rasql_test

import (
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/stretchr/testify/require"
)

type registryCodec struct{}

func (registryCodec) Encode(value any) (driver.Value, error) { return value, nil }
func (registryCodec) Decode(source any, destination any) error {
	*destination.(*string) = source.(string)
	return nil
}

func TestCodecRegistry(t *testing.T) {
	registry, err := rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{"text": registryCodec{}})
	require.NoError(t, err)
	codec, ok := registry.Lookup("text")
	require.True(t, ok)
	require.NotNil(t, codec)
	_, err = rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{"": registryCodec{}})
	require.Error(t, err)
}

func TestCodecErrors(t *testing.T) {
	// A column that no codec decoded failed inside rasql's own conversion, so its
	// message names the cause. TestCodecErrors/"codec cause text stays hidden" covers the other
	// half of the same rule, where a codec produced the error and its text stays
	// out of the message.
	t.Run("decode error names its cause without a codec", func(t *testing.T) {
		cause := errors.New(`expected int64, got []uint8`)
		err := &rasql.DecodeError{Column: "total", Err: cause}
		require.True(t, errors.Is(err, cause))
		require.Equal(t, `decode column "total" failed: expected int64, got []uint8`, err.Error())
	})

	t.Run("codec cause text stays hidden", func(t *testing.T) {
		secret := errors.New("secret value should not be printed")
		err := &rasql.DecodeError{Column: "payload", Codec: "text", Err: secret}
		require.True(t, errors.Is(err, secret))
		require.False(t, strings.Contains(err.Error(), "secret value"))
		err2 := &rasql.EncodeError{Index: 2, Codec: "text", Err: secret}
		require.True(t, errors.Is(err2, secret))
		require.False(t, strings.Contains(err2.Error(), "secret value"))
	})
}
