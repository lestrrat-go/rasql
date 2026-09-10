package cursorcodec_test

import (
	"encoding/base64"
	"testing"

	"github.com/lestrrat-go/rasql/internal/cursorcodec"
	"github.com/stretchr/testify/require"
)

// validEnvelope builds a one-field envelope with an empty codec and returns
// both the encoded text and its raw bytes, so a test can mutate one byte and
// see how the reader answers.
func validEnvelope(t *testing.T) (string, []byte) {
	t.Helper()
	fields := []cursorcodec.Field{{Direction: 1}}
	values := []cursorcodec.Value{{Present: true, Data: []byte{1}}}
	encoded, err := cursorcodec.EncodeEnvelope([32]byte{}, fields, values)
	require.NoError(t, err)
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	require.NoError(t, err)
	return encoded, raw
}

func TestEnvelope(t *testing.T) {
	t.Run("round-trips fields and values", func(t *testing.T) {
		fingerprint := [32]byte{1, 2, 3}
		fields := []cursorcodec.Field{
			{Direction: 1, Codec: "some.codec"},
			{Direction: 2, Nullable: true, Nulls: 2},
		}
		values := []cursorcodec.Value{{Present: true, Data: []byte("payload")}, {}}
		encoded, err := cursorcodec.EncodeEnvelope(fingerprint, fields, values)
		require.NoError(t, err)

		decoded, err := cursorcodec.DecodeEnvelope(encoded)
		require.NoError(t, err)
		require.Equal(t, fingerprint, decoded.Fingerprint)
		require.Equal(t, fields, decoded.Fields)
		require.Len(t, decoded.Values, 2)
		require.True(t, decoded.Values[0].Present)
		require.Equal(t, []byte("payload"), decoded.Values[0].Data)
		require.False(t, decoded.Values[1].Present)
		require.Empty(t, decoded.Values[1].Data)
	})

	t.Run("rejects every bounded malformed form", func(t *testing.T) {
		_, raw := validEnvelope(t)
		forms := map[string][]byte{
			"version":           append([]byte{2}, raw[1:]...),
			"short fingerprint": append([]byte{1}, raw[1:10]...),
			"truncated value":   raw[:len(raw)-1],
			"oversized value": func() []byte {
				value := append([]byte(nil), raw...)
				value[40], value[41], value[42], value[43] = 0xff, 0xff, 0xff, 0xff
				return value
			}(),
			"trailing bytes": append(append([]byte(nil), raw...), 0),
			"non-canonical nullable marker": func() []byte {
				value := append([]byte(nil), raw...)
				value[35] = 2
				return value
			}(),
			"non-canonical presence marker": func() []byte {
				value := append([]byte(nil), raw...)
				value[38] = 2
				return value
			}(),
			"absent value carrying a payload": func() []byte {
				value := append([]byte(nil), raw...)
				value[38] = 0
				value[39], value[40], value[41], value[42] = 0, 0, 0, 1
				return value
			}(),
		}
		for name, malformed := range forms {
			t.Run(name, func(t *testing.T) {
				_, err := cursorcodec.DecodeEnvelope(base64.RawURLEncoding.EncodeToString(malformed))
				require.Error(t, err)
			})
		}

		_, err := cursorcodec.DecodeEnvelope("$")
		require.Error(t, err, "text that is not base64")

		tooLarge := make([]byte, cursorcodec.MaxEnvelopeBytes+1)
		_, err = cursorcodec.DecodeEnvelope(base64.RawURLEncoding.EncodeToString(tooLarge))
		require.Error(t, err, "an envelope past the size cap")
	})

	t.Run("rejects what the format cannot represent", func(t *testing.T) {
		field := cursorcodec.Field{Direction: 1}

		tooMany := make([]cursorcodec.Field, 256)
		_, err := cursorcodec.EncodeEnvelope([32]byte{}, tooMany, make([]cursorcodec.Value, 256))
		require.Error(t, err, "more than 255 fields")

		_, err = cursorcodec.EncodeEnvelope([32]byte{}, []cursorcodec.Field{field}, nil)
		require.Error(t, err, "fewer values than fields")

		largeCodec := cursorcodec.Field{Codec: string(make([]byte, 256))}
		_, err = cursorcodec.EncodeEnvelope([32]byte{}, []cursorcodec.Field{largeCodec}, []cursorcodec.Value{{}})
		require.Error(t, err, "a codec name past 255 bytes")

		_, err = cursorcodec.EncodeEnvelope([32]byte{}, []cursorcodec.Field{field},
			[]cursorcodec.Value{{Present: true, Data: make([]byte, 65535)}})
		require.Error(t, err, "a value past the per-field cap")

		_, err = cursorcodec.EncodeEnvelope([32]byte{}, []cursorcodec.Field{field},
			[]cursorcodec.Value{{Present: false, Data: []byte{1}}})
		require.Error(t, err, "an absent value carrying a payload")
	})

	t.Run("a valid envelope stays inside the size cap", func(t *testing.T) {
		_, raw := validEnvelope(t)
		require.LessOrEqual(t, len(raw), cursorcodec.MaxEnvelopeBytes)
	})
}
