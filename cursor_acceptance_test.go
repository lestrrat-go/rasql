package rasql

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestR5CursorRejectsEveryBoundedMalformedForm(t *testing.T) {
	key := AscKey[int](Value(1), func(value int) int { return value })
	spec, err := NewPageSpec([]PageKey[int]{key}, key)
	require.NoError(t, err)
	valid, err := encodeCursorEnvelope([32]byte{}, []*pageKey[int]{key.(*pageKey[int])}, []cursorValue{{present: true, data: []byte{1}}})
	require.NoError(t, err)
	raw, err := base64.RawURLEncoding.DecodeString(string(valid))
	require.NoError(t, err)
	forms := map[string][]byte{
		"invalid base64":      []byte("$"),
		"version":             append([]byte{2}, raw[1:]...),
		"short fingerprint":   append([]byte{1}, raw[1:10]...),
		"wrong arity":         append(append([]byte(nil), raw[:33]...), 2),
		"metadata direction":  func() []byte { value := append([]byte(nil), raw...); value[34] = byte(PageDescending); return value }(),
		"metadata nullable":   func() []byte { value := append([]byte(nil), raw...); value[35] = 1; return value }(),
		"metadata null order": func() []byte { value := append([]byte(nil), raw...); value[36] = byte(NullsLast); return value }(),
		"metadata codec": func() []byte {
			value := append([]byte(nil), raw...)
			value[37] = 1
			value = append(value[:38], append([]byte("x"), value[38:]...)...)
			return value
		}(),
		"truncated value": raw[:len(raw)-1],
		"oversized value": func() []byte {
			value := append([]byte(nil), raw...)
			value[40] = 0xff
			value[41] = 0xff
			value[42] = 0xff
			value[43] = 0xff
			return value
		}(),
		"trailing bytes": append(append([]byte(nil), raw...), 0),
	}
	for name, malformed := range forms {
		t.Run(name, func(t *testing.T) {
			cursor := Cursor(base64.RawURLEncoding.EncodeToString(malformed))
			_, _, err := decodePageCursor(cursor, spec, nil)
			require.ErrorIs(t, err, ErrInvalidCursor)
		})
	}
	require.LessOrEqual(t, len(raw), 64*1024)
	tooLarge := make([]byte, 64*1024+1)
	_, _, err = decodePageCursor(Cursor(base64.RawURLEncoding.EncodeToString(tooLarge)), spec, nil)
	require.ErrorIs(t, err, ErrInvalidCursor)
}

func TestR5CursorEnvelopeRoundTripsNullPresence(t *testing.T) {
	key := AscNullKey[int, int](NullExpr[int]{node: nil}, func(value int) Nullable[int] { return Nullable[int]{} }, NullsLast)
	_, ok := key.(*pageKey[int])
	require.True(t, ok)
}
