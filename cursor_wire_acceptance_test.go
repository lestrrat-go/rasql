package rasql

import (
	"encoding/base64"
	"math"
	"reflect"
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

type r5NamedBool bool
type r5NamedString string
type r5NamedInt8 int8
type r5NamedUint8 uint8

func TestR5BuiltinCursorPreservesNamedTypesAndRejectsOverflow(t *testing.T) {
	encoded, err := encodeBuiltinCursor(r5NamedBool(true))
	require.NoError(t, err)
	decoded, err := decodeBuiltinCursor(encoded, reflect.TypeOf(r5NamedBool(false)))
	require.NoError(t, err)
	require.IsType(t, r5NamedBool(false), decoded)
	encoded, err = encodeBuiltinCursor(r5NamedString("value"))
	require.NoError(t, err)
	decoded, err = decodeBuiltinCursor(encoded, reflect.TypeOf(r5NamedString("")))
	require.NoError(t, err)
	require.IsType(t, r5NamedString(""), decoded)
	intBytes, err := encodeBuiltinCursor(int64(128))
	require.NoError(t, err)
	_, err = decodeBuiltinCursor(intBytes, reflect.TypeOf(r5NamedInt8(0)))
	require.Error(t, err)
	uintBytes, err := encodeBuiltinCursor(uint64(256))
	require.NoError(t, err)
	_, err = decodeBuiltinCursor(uintBytes, reflect.TypeOf(r5NamedUint8(0)))
	require.Error(t, err)
	_, err = encodeBuiltinCursor(math.NaN())
	require.Error(t, err)
}

func TestR5FingerprintIncludesCanonicalColumnParameters(t *testing.T) {
	key := &pageKey[int]{direction: PageAscending, term: OrderTerm{source: "items"}}
	statement := stmt.New(sqltext.Text("SELECT id FROM items"))
	base := func(column schema.ColumnType) [32]byte {
		fingerprint, err := pageFingerprint("sqlite", statement.SQL(), mustRuntimeSchema(t, ResultColumn{Name: "value", Type: column}), statement, []*pageKey[int]{key}, nil)
		require.NoError(t, err)
		return fingerprint
	}
	require.NotEqual(t, base(schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2)}), base(schema.DecimalType{Precision: 12, Scale: schema.NewDecimalScale(2)}))
	require.NotEqual(t, base(schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2)}), base(schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(3)}))
	require.NotEqual(t, base(schema.TextType{Width: schema.NewTextWidth(10)}), base(schema.TextType{Width: schema.NewTextWidth(20)}))
	require.NotEqual(t, base(schema.TextType{Width: schema.NewTextWidth(10)}), base(schema.TextType{Width: schema.NewTextWidth(10), Fixed: true}))
	require.NotEqual(t, base(schema.IntegerType{}), base(schema.IntegerType{Unsigned: true}))
	require.Equal(t, base(schema.IntegerType{}), base(schema.IntegerType{}))
}

func TestR5CursorEnvelopeRejectsNonCanonicalMarkersAndBounds(t *testing.T) {
	key := AscKey[int](Value(1), func(value int) int { return value }).(*pageKey[int])
	valid, err := encodeCursorEnvelope([32]byte{}, []*pageKey[int]{key}, []cursorValue{{present: true, data: []byte{1}}})
	require.NoError(t, err)
	raw, err := base64.RawURLEncoding.DecodeString(string(valid))
	require.NoError(t, err)
	spec, err := NewPageSpec([]PageKey[int]{key}, key)
	require.NoError(t, err)
	for _, index := range []int{35, 38} {
		mutated := append([]byte(nil), raw...)
		mutated[index] = 2
		_, _, err = decodePageCursor(Cursor(base64.RawURLEncoding.EncodeToString(mutated)), spec, nil)
		require.ErrorIs(t, err, ErrInvalidCursor)
	}
	mutated := append([]byte(nil), raw...)
	mutated[38] = 0
	mutated[39] = 0
	mutated[40], mutated[41], mutated[42], mutated[43] = 0, 0, 0, 1
	mutated = append(mutated, 1)
	_, _, err = decodePageCursor(Cursor(base64.RawURLEncoding.EncodeToString(mutated)), spec, nil)
	require.ErrorIs(t, err, ErrInvalidCursor)
	tooMany := make([]*pageKey[int], 256)
	for i := range tooMany {
		tooMany[i] = key
	}
	_, err = encodeCursorEnvelope([32]byte{}, tooMany, make([]cursorValue, 256))
	require.Error(t, err)
	largeCodec := &pageKey[int]{codec: string(make([]byte, 256))}
	_, err = encodeCursorEnvelope([32]byte{}, []*pageKey[int]{largeCodec}, []cursorValue{{present: false}})
	require.Error(t, err)
	_, err = encodeCursorEnvelope([32]byte{}, []*pageKey[int]{key}, []cursorValue{{present: true, data: make([]byte, 65535)}})
	require.Error(t, err)
}
