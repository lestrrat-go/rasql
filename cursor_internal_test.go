package rasql

import (
	"encoding/base64"
	"encoding/binary"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestCursor(t *testing.T) {
	t.Run("rejects every bounded malformed form", func(t *testing.T) {
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
	})

	t.Run("envelope round-trips null presence", func(t *testing.T) {
		key := AscNullKey[int, int](NullExpr[int]{node: nil}, func(value int) Nullable[int] { return Nullable[int]{} }, NullsLast)
		_, ok := key.(*pageKey[int])
		require.True(t, ok)
	})

	t.Run("envelope rejects non-canonical markers and bounds", func(t *testing.T) {
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
	})

	t.Run("builtin values preserve named types and reject overflow", func(t *testing.T) {
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
		for _, value := range []float64{math.Inf(1), -math.Inf(1)} {
			encoded, encodeErr := encodeBuiltinCursor(value)
			require.NoError(t, encodeErr)
			decoded, decodeErr := decodeBuiltinCursor(encoded, reflect.TypeOf(float32(0)))
			require.NoError(t, decodeErr)
			require.Equal(t, float32(value), decoded)
		}
		malformedNaN := make([]byte, 8)
		binary.BigEndian.PutUint64(malformedNaN, math.Float64bits(math.NaN())^(1<<63))
		_, err = decodeBuiltinCursor(malformedNaN, reflect.TypeOf(float64(0)))
		require.Error(t, err)
		encoded, err = encodeBuiltinCursor(1e40)
		require.NoError(t, err)
		_, err = decodeBuiltinCursor(encoded, reflect.TypeOf(float32(0)))
		require.Error(t, err)
	})

	t.Run("builtin time round-trips the full database range", func(t *testing.T) {
		for _, want := range []time.Time{
			time.Date(1, time.January, 1, 0, 0, 0, 123456789, time.FixedZone("offset", 9*60*60)),
			time.Date(9999, time.December, 31, 23, 59, 59, 987654321, time.FixedZone("offset", -8*60*60)),
			time.Now().Add(123456789 * time.Nanosecond),
		} {
			encoded, err := encodeBuiltinCursor(want)
			require.NoError(t, err)
			require.Len(t, encoded, 12)
			got, err := decodeBuiltinCursor(encoded, reflect.TypeOf(time.Time{}))
			require.NoError(t, err)
			require.Equal(t, want.UTC(), got)
		}
	})

	t.Run("a malformed float cursor is rejected before the query runs", func(t *testing.T) {
		keyExpr := Value(float64(1))
		key := AscKey[r5FloatPageRow](keyExpr, func(row r5FloatPageRow) float64 { return row.Value }).(*pageKey[r5FloatPageRow])
		spec, err := NewPageSpec([]PageKey[r5FloatPageRow]{key}, key)
		require.NoError(t, err)
		table, err := ReadTableOf[r5FloatPageRow](schema.TableDef{Name: "float_page_rows", Columns: []schema.ColumnDef{{Name: "value", Type: schema.FloatType{}}}})
		require.NoError(t, err)
		relation, err := SourceOf(table, "p")
		require.NoError(t, err)
		value, err := BindColumn[r5FloatPageRow, float64](relation, "value", "")
		require.NoError(t, err)
		resultSchema, err := NewResultSchema(ResultColumn{Name: "value", Type: schema.FloatType{}})
		require.NoError(t, err)
		projection, err := NewProjection([]ProjectionItem{Item("value", value.Expr(), schema.FloatType{}, "")}, r5FloatPageDecoder{schema: resultSchema})
		require.NoError(t, err)
		query := Select(relation.Source(), projection)
		data := make([]byte, 8)
		binary.BigEndian.PutUint64(data, math.Float64bits(math.NaN())^(1<<63))
		cursor, err := encodeCursorEnvelope([32]byte{}, []*pageKey[r5FloatPageRow]{key}, []cursorValue{{present: true, data: data}})
		require.NoError(t, err)
		executor := runtimeExecutor(t, [][]any{{float64(1)}})
		raw := executor.(profiledExecutor).Executor.(*runtimeFakeExecutor)
		_, err = PageAfter(t.Context(), executor, query, spec, PagePolicy{DefaultLimit: 1, MaxLimit: 2}, PageRequest{Limit: 1, After: cursor})
		require.ErrorIs(t, err, ErrInvalidCursor)
		require.Zero(t, raw.calls.Load())
	})
}

type r5NamedBool bool
type r5NamedString string
type r5NamedInt8 int8
type r5NamedUint8 uint8

type r5FloatPageRow struct{ Value float64 }

type r5FloatPageDecoder struct{ schema ResultSchema }

func (d r5FloatPageDecoder) ResultSchema() ResultSchema { return d.schema }
func (r5FloatPageDecoder) Presence() []Presence         { return nil }
func (d r5FloatPageDecoder) DecodeRow(source ScanSource, row *r5FloatPageRow) error {
	return source.Scan(&row.Value)
}
