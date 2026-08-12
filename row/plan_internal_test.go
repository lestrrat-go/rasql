package row

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

// planOnceFieldedRow is declared only in this test, so its reflect.Type is
// used by no other test in the package and decodePlanBuilds can be attributed
// to it alone.
type planOnceFieldedRow struct {
	ID int64
}

// planOnceEmbeddedDecoder is unexported, so the field-mapping walk skips the
// anonymous field it backs and planOnceEmbedWithFields is field-mapped only
// through the field it declares of its own.
type planOnceEmbeddedDecoder struct {
	ID int64
}

func (d *planOnceEmbeddedDecoder) DecodeRow(Dynamic) error {
	return nil
}

// planOnceEmbedWithFields embeds a Decoder and declares a field of its own, so
// building its plan exercises fieldsShadowEmbeddedDecoder and, inside it, the
// internal/method.Declared call that tells a declared DecodeRow apart from a
// promoted one.
type planOnceEmbedWithFields struct {
	planOnceEmbeddedDecoder
	Extra int64 `rasql:"extra"`
}

// planOnceEmptyTag carries a rasql tag with an empty column name, which
// buildPlan turns into a permanent per-type error.
type planOnceEmptyTag struct {
	Name string `rasql:""`
}

// TestPlanIsBuiltOncePerType decodes each row type many times over and checks
// that decodePlanBuilds advances by exactly one per type, proving planFor
// caches rather than rebuilds on every Decode call.
func TestPlanIsBuiltOncePerType(t *testing.T) {
	t.Run("field-mapped struct", func(t *testing.T) {
		source, err := NewDynamic([]string{"id"}, []any{int64(42)})
		require.NoError(t, err)

		before := decodePlanBuilds.Load()
		for range 100 {
			decoded, err := Decode[planOnceFieldedRow](source)
			require.NoError(t, err)
			require.Equal(t, int64(42), decoded.ID)
		}
		require.Equal(t, int64(1), decodePlanBuilds.Load()-before)
	})

	t.Run("struct embedding a Decoder with fields of its own", func(t *testing.T) {
		source, err := NewDynamic([]string{"extra"}, []any{int64(7)})
		require.NoError(t, err)

		before := decodePlanBuilds.Load()
		for range 100 {
			decoded, err := Decode[planOnceEmbedWithFields](source)
			require.NoError(t, err)
			require.Equal(t, int64(7), decoded.Extra)
			// The embedded DecodeRow is shadowed by the declared field, so it
			// never ran and ID stays zero.
			require.Zero(t, decoded.ID)
		}
		require.Equal(t, int64(1), decodePlanBuilds.Load()-before)
	})

	t.Run("struct whose plan is an error", func(t *testing.T) {
		source, err := NewDynamic([]string{"name"}, []any{"Ada"})
		require.NoError(t, err)

		before := decodePlanBuilds.Load()
		_, firstErr := Decode[planOnceEmptyTag](source)
		_, secondErr := Decode[planOnceEmptyTag](source)
		require.Error(t, firstErr)
		require.EqualError(t, secondErr, firstErr.Error())
		require.Equal(t, int64(1), decodePlanBuilds.Load()-before)
	})
}

// planPlainStruct implements no Decoder at all.
type planPlainStruct struct {
	ID int64
}

// planNonStructRow is not a struct but declares DecodeRow on its pointer type,
// the shape TestDecodePrefersRowDecoder's "accepts a non-struct destination"
// subtest covers through Decode.
type planNonStructRow int64

func (r *planNonStructRow) DecodeRow(Dynamic) error {
	return nil
}

// planValueReceiverRow declares DecodeRow with a value receiver, so both
// planValueReceiverRow and *planValueReceiverRow implement Decoder.
type planValueReceiverRow struct {
	ID int64
}

func (planValueReceiverRow) DecodeRow(Dynamic) error {
	return nil
}

// planPointerReceiverRow declares DecodeRow with a pointer receiver, so only
// *planPointerReceiverRow implements Decoder.
type planPointerReceiverRow struct {
	ID int64
}

func (r *planPointerReceiverRow) DecodeRow(Dynamic) error {
	return nil
}

// planEmbedNoFields embeds a Decoder and declares no field of its own, so the
// embedded DecodeRow is promoted rather than shadowed.
type planEmbedNoFields struct {
	planPointerReceiverRow
}

// planEmbedWithFields embeds a Decoder and declares a field of its own, so the
// embedded DecodeRow is shadowed by the field it declares.
type planEmbedWithFields struct {
	planPointerReceiverRow
	Extra int64
}

// TestPointerImplementsDecoderMatchesTypeAssertion is what makes it safe for
// buildPlan to decide "does *T implement Decoder" with
// reflect.PointerTo(rowType).Implements(decoderType) instead of the type
// assertion any(&result).(Decoder) that Decode used before this change: for
// every shape a row type can take -- a plain struct, a non-struct with a
// pointer-receiver DecodeRow, a value-receiver DecodeRow, a pointer-receiver
// DecodeRow, an embedding that promotes DecodeRow, an embedding that shadows
// it, a pointer type, and an interface type -- the two report the same
// answer.
func TestPointerImplementsDecoderMatchesTypeAssertion(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeFor[planPlainStruct](),
		reflect.TypeFor[planNonStructRow](),
		reflect.TypeFor[planValueReceiverRow](),
		reflect.TypeFor[planPointerReceiverRow](),
		reflect.TypeFor[planEmbedNoFields](),
		reflect.TypeFor[planEmbedWithFields](),
		reflect.TypeFor[*planPlainStruct](),
		reflect.TypeFor[Decoder](),
	}
	for _, rowType := range types {
		t.Run(rowType.String(), func(t *testing.T) {
			// reflect.New(rowType) allocates an addressable zero value of
			// rowType and returns its pointer, exactly what &result is for a
			// generic Decode[T] call with T == rowType, so asserting its
			// Interface() against Decoder is the same check any(&result).(Decoder)
			// makes.
			_, wantsDecoder := reflect.New(rowType).Interface().(Decoder)
			implementsDecoder := reflect.PointerTo(rowType).Implements(decoderType)
			require.Equal(t, wantsDecoder, implementsDecoder)
		})
	}
}
