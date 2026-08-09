package row_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql/row"
	"github.com/stretchr/testify/require"
)

func TestTypedColumnsDecodeValues(t *testing.T) {
	createdAt := time.Date(2026, time.August, 1, 12, 30, 0, 0, time.UTC)
	inputBytes := []byte("payload")
	result, err := row.New(
		[]string{"active", "id", "ratio", "name", "payload", "created_at"},
		[]any{true, int64(42), 1.5, []byte("Ada"), inputBytes, createdAt},
	)
	require.NoError(t, err)

	active, err := row.Bool("active")
	require.NoError(t, err)
	id, err := row.Int64("id")
	require.NoError(t, err)
	ratio, err := row.Float64("ratio")
	require.NoError(t, err)
	name, err := row.String("name")
	require.NoError(t, err)
	payload, err := row.Bytes("payload")
	require.NoError(t, err)
	when, err := row.Time("created_at")
	require.NoError(t, err)

	gotActive, err := active.Get(result)
	require.NoError(t, err)
	require.True(t, gotActive)
	gotID, err := id.Get(result)
	require.NoError(t, err)
	require.Equal(t, int64(42), gotID)
	gotRatio, err := ratio.Get(result)
	require.NoError(t, err)
	require.Equal(t, 1.5, gotRatio)
	gotName, err := name.Get(result)
	require.NoError(t, err)
	require.Equal(t, "Ada", gotName)
	gotPayload, err := payload.Get(result)
	require.NoError(t, err)
	require.Equal(t, []byte("payload"), gotPayload)
	gotWhen, err := when.Get(result)
	require.NoError(t, err)
	require.Equal(t, createdAt, gotWhen)

	inputBytes[0] = 'P'
	require.Equal(t, []byte("payload"), gotPayload)
}

func TestTypedColumnsDecodeSQLiteValues(t *testing.T) {
	createdAt := time.Date(2026, time.August, 1, 12, 30, 0, 0, time.UTC)
	result, err := row.New(
		[]string{"active", "created_at"},
		[]any{int64(1), createdAt.String()},
	)
	require.NoError(t, err)

	active, err := row.Bool("active")
	require.NoError(t, err)
	when, err := row.Time("created_at")
	require.NoError(t, err)

	gotActive, err := active.Get(result)
	require.NoError(t, err)
	require.True(t, gotActive)
	gotWhen, err := when.Get(result)
	require.NoError(t, err)
	require.Equal(t, createdAt, gotWhen)
}

func TestNullableDecoderPreservesNull(t *testing.T) {
	result, err := row.New([]string{"name"}, []any{nil})
	require.NoError(t, err)
	name, err := row.NewColumn("name", row.Nullable(row.ColumnDecoderFunc[string](func(value any) (string, error) {
		return value.(string), nil
	})))
	require.NoError(t, err)

	got, err := name.Get(result)
	require.NoError(t, err)
	require.False(t, got.Valid)
}

func TestNullableDecoderPreservesNullWithNilDecoder(t *testing.T) {
	result, err := row.New([]string{"name"}, []any{nil})
	require.NoError(t, err)
	var decoder row.ColumnDecoder[string]
	name, err := row.NewColumn("name", row.Nullable(decoder))
	require.NoError(t, err)

	got, err := name.Get(result)
	require.NoError(t, err)
	require.False(t, got.Valid)
}

func TestNullableDecoderRejectsNilDecoder(t *testing.T) {
	result, err := row.New([]string{"name"}, []any{"Ada"})
	require.NoError(t, err)
	var typedNil row.ColumnDecoderFunc[string]

	for _, test := range []struct {
		name    string
		decoder row.ColumnDecoder[string]
	}{
		{name: "nil interface"},
		{name: "typed nil", decoder: typedNil},
	} {
		t.Run(test.name, func(t *testing.T) {
			name, err := row.NewColumn("name", row.Nullable(test.decoder))
			require.NoError(t, err)

			_, err = name.Get(result)
			require.ErrorContains(t, err, "nullable decoder must not be nil")
		})
	}
}

func TestNewColumnRejectsTypedNilDecoder(t *testing.T) {
	var decoder row.ColumnDecoderFunc[string]
	_, err := row.NewColumn("name", decoder)
	require.ErrorContains(t, err, "decoder for column \"name\" must not be nil")
}

func TestTypedColumnsRejectWrongType(t *testing.T) {
	result, err := row.New([]string{"id"}, []any{"42"})
	require.NoError(t, err)
	id, err := row.Int64("id")
	require.NoError(t, err)

	_, err = id.Get(result)
	require.Error(t, err)
}

func TestGetAndDecodePopulateTypedValues(t *testing.T) {
	result, err := row.New(
		[]string{"id", "email", "nickname"},
		[]any{int64(42), []byte("ada@example.com"), nil},
	)
	require.NoError(t, err)

	id, err := row.Get[int64](result, "id")
	require.NoError(t, err)
	require.Equal(t, int64(42), id)

	type user struct {
		ID       int64
		Email    string
		Nickname *string
	}
	decoded, err := row.Decode[user](result)
	require.NoError(t, err)
	require.Equal(t, int64(42), decoded.ID)
	require.Equal(t, "ada@example.com", decoded.Email)
	require.Nil(t, decoded.Nickname)

	type summary struct {
		UserID int64 `rasql:"id"`
	}
	aliased, err := row.Decode[summary](result)
	require.NoError(t, err)
	require.Equal(t, int64(42), aliased.UserID)
}

func TestDecodeRejectsMissingColumnsAndUnsupportedDestinations(t *testing.T) {
	result, err := row.New([]string{"id"}, []any{int64(42)})
	require.NoError(t, err)

	type user struct {
		Email string `rasql:"email"`
	}
	_, err = row.Decode[user](result)
	require.Error(t, err)
	_, err = row.Decode[string](result)
	require.Error(t, err)
}

func TestAssignDecodesIntoDestination(t *testing.T) {
	result, err := row.New(
		[]string{"id", "email", "nickname"},
		[]any{int64(42), []byte("ada@example.com"), nil},
	)
	require.NoError(t, err)

	var id int64
	require.NoError(t, row.Assign(result, "id", &id))
	require.Equal(t, int64(42), id)

	var email string
	require.NoError(t, row.Assign(result, "email", &email))
	require.Equal(t, "ada@example.com", email)

	// A nullable column decodes into a nil pointer rather than failing.
	nickname := new(string)
	require.NoError(t, row.Assign(result, "nickname", &nickname))
	require.Nil(t, nickname)

	t.Run("missing column", func(t *testing.T) {
		var missing int64
		err := row.Assign(result, "nope", &missing)
		require.ErrorContains(t, err, `row: column "nope" is not present`)
	})

	t.Run("nil destination", func(t *testing.T) {
		var destination *int64
		err := row.Assign(result, "id", destination)
		require.ErrorContains(t, err, `row: destination for column "id" must not be nil`)
	})

	t.Run("conversion failure", func(t *testing.T) {
		var wrong bool
		err := row.Assign(result, "email", &wrong)
		require.ErrorContains(t, err, `row: decode column "email"`)
	})
}

// TestAssignDecodesBoolFromAnyNonzeroInteger pins MySQL's documented boolean
// semantics: TINYINT accepts the full signed range regardless of its display
// width, and any nonzero value (not just 1) is true. Both the text and binary
// driver protocols deliver int64 for a tiny field, and unsigned driver values
// must be covered too.
func TestAssignDecodesBoolFromAnyNonzeroInteger(t *testing.T) {
	result, err := row.New(
		[]string{"zero", "two", "unsigned_two", "negative_one", "not_an_integer"},
		[]any{int64(0), int64(2), uint64(2), int64(-1), "true"},
	)
	require.NoError(t, err)

	var zero bool
	require.NoError(t, row.Assign(result, "zero", &zero))
	require.False(t, zero)

	var two bool
	require.NoError(t, row.Assign(result, "two", &two))
	require.True(t, two)

	var unsignedTwo bool
	require.NoError(t, row.Assign(result, "unsigned_two", &unsignedTwo))
	require.True(t, unsignedTwo)

	var negativeOne bool
	require.NoError(t, row.Assign(result, "negative_one", &negativeOne))
	require.True(t, negativeOne)

	var notAnInteger bool
	err = row.Assign(result, "not_an_integer", &notAnInteger)
	require.ErrorContains(t, err, `row: decode column "not_an_integer"`)
}

// TestAssignDecodesIntegersAcrossSignedness covers the uint64 field the
// generator emits for an unsigned integer column. Which signedness a driver
// delivers is the driver's choice rather than the column's:
// go-sql-driver/mysql hands back uint64 only for a BIGINT UNSIGNED column and
// int64 for a narrower unsigned one, so a uint64 field has to accept both. A
// value that does not fit the destination is an error, never a wrap.
func TestAssignDecodesIntegersAcrossSignedness(t *testing.T) {
	result, err := row.New(
		[]string{"big_unsigned", "narrow_unsigned", "negative", "small_unsigned"},
		[]any{uint64(18446744073709551615), int64(42), int64(-1), uint64(7)},
	)
	require.NoError(t, err)

	t.Run("uint64 field takes an unsigned driver value", func(t *testing.T) {
		var destination uint64
		require.NoError(t, row.Assign(result, "big_unsigned", &destination))
		require.Equal(t, uint64(18446744073709551615), destination)
	})

	t.Run("uint64 field takes a signed driver value", func(t *testing.T) {
		var destination uint64
		require.NoError(t, row.Assign(result, "narrow_unsigned", &destination))
		require.Equal(t, uint64(42), destination)
	})

	t.Run("uint64 field rejects a negative value", func(t *testing.T) {
		var destination uint64
		err := row.Assign(result, "negative", &destination)
		require.ErrorContains(t, err, "-1 overflows uint64")
	})

	t.Run("int64 field takes a small unsigned value", func(t *testing.T) {
		var destination int64
		require.NoError(t, row.Assign(result, "small_unsigned", &destination))
		require.Equal(t, int64(7), destination)
	})

	// This is the range the whole change exists for: an int64 field cannot
	// hold what a BIGINT UNSIGNED column stores above 9223372036854775807, and
	// says so instead of wrapping to a negative number.
	t.Run("int64 field rejects a value above its range", func(t *testing.T) {
		var destination int64
		err := row.Assign(result, "big_unsigned", &destination)
		require.ErrorContains(t, err, "18446744073709551615 overflows int64")
	})
}

// TestAssignRejectsExactDecimalSourcesForFloat64 records why rasql maps NUMERIC
// and DECIMAL columns to a rejected inspection rather than schema.FloatType: the
// drivers hand back the exact decimal as a string or []byte, and a float64
// destination accepts neither, so the generated field could never decode it.
func TestAssignRejectsExactDecimalSourcesForFloat64(t *testing.T) {
	t.Run("string source", func(t *testing.T) {
		result, err := row.New([]string{"pg_numeric"}, []any{"12.50"})
		require.NoError(t, err)

		var destination float64
		err = row.Assign(result, "pg_numeric", &destination)
		require.ErrorContains(t, err, `row: decode column "pg_numeric": expected float64, got string`)
	})

	t.Run("byte slice source", func(t *testing.T) {
		result, err := row.New([]string{"mysql_decimal"}, []any{[]byte("12.50")})
		require.NoError(t, err)

		var destination float64
		err = row.Assign(result, "mysql_decimal", &destination)
		require.ErrorContains(t, err, `row: decode column "mysql_decimal": expected float64, got []uint8`)
	})
}

// TestAssignDecodesExactDecimalIntoString is the mirror of
// TestAssignRejectsExactDecimalSourcesForFloat64: the same two driver values
// that a float64 destination rejects both decode cleanly into a string, with
// every digit unchanged. Nothing in row/ changes for schema.DecimalType, so
// this test exists to pin the behavior that design depends on.
func TestAssignDecodesExactDecimalIntoString(t *testing.T) {
	t.Run("string source", func(t *testing.T) {
		result, err := row.New([]string{"pg_numeric"}, []any{"1234.5678901234567890"})
		require.NoError(t, err)

		var destination string
		require.NoError(t, row.Assign(result, "pg_numeric", &destination))
		require.Equal(t, "1234.5678901234567890", destination)
	})

	t.Run("byte slice source", func(t *testing.T) {
		result, err := row.New([]string{"mysql_decimal"}, []any{[]byte("1234.5678901234567890")})
		require.NoError(t, err)

		var destination string
		require.NoError(t, row.Assign(result, "mysql_decimal", &destination))
		require.Equal(t, "1234.5678901234567890", destination)
	})
}

// selfDecodedUser maps its own columns and carries a tag that names a different
// column, so a decode through the tag is distinguishable from one through the method.
type selfDecodedUser struct {
	ID    int64 `rasql:"nickname"`
	Email string
	Extra string
}

func (u *selfDecodedUser) DecodeRow(r row.Dynamic) error {
	if err := row.Assign(r, "id", &u.ID); err != nil {
		return err
	}
	if err := row.Assign(r, "email", &u.Email); err != nil {
		return err
	}
	u.Extra = "derived"
	return nil
}

// failingDecoder reports an error from DecodeRow so Decode's wrapping is visible.
type failingDecoder struct{}

func (failingDecoder) DecodeRow(row.Dynamic) error {
	return errors.New("mapping failed")
}

// selfDecodedCount is not a struct, which the reflection path rejects.
type selfDecodedCount int64

func (c *selfDecodedCount) DecodeRow(r row.Dynamic) error {
	return row.Assign(r, "id", (*int64)(c))
}

func TestDecodePrefersRowDecoder(t *testing.T) {
	result, err := row.New(
		[]string{"id", "email", "nickname"},
		[]any{int64(42), []byte("ada@example.com"), int64(7)},
	)
	require.NoError(t, err)

	decoded, err := row.Decode[selfDecodedUser](result)
	require.NoError(t, err)
	// The tag names "nickname", so 42 proves DecodeRow ran instead of the tag path.
	require.Equal(t, int64(42), decoded.ID)
	require.Equal(t, "ada@example.com", decoded.Email)
	require.Equal(t, "derived", decoded.Extra)

	t.Run("propagates the error", func(t *testing.T) {
		_, err := row.Decode[failingDecoder](result)
		require.ErrorContains(t, err, "mapping failed")
		require.ErrorContains(t, err, "row: decode")
	})

	t.Run("accepts a non-struct destination", func(t *testing.T) {
		count, err := row.Decode[selfDecodedCount](result)
		require.NoError(t, err)
		require.Equal(t, selfDecodedCount(42), count)
	})

	t.Run("embedded without fields of its own", func(t *testing.T) {
		// The wrapper declares no field, so only the promoted DecodeRow can map
		// it and these values prove it ran.
		decoded, err := row.Decode[decoderWrapper](result)
		require.NoError(t, err)
		require.Equal(t, int64(42), decoded.ID)
		require.Equal(t, "ada@example.com", decoded.Email)
		require.Equal(t, "derived", decoded.Extra)
	})
}

// decoderWrapper embeds a row type that decodes itself and declares no field of
// its own, so only the promoted DecodeRow can map it.
type decoderWrapper struct {
	selfDecodedUser
}

// fieldedDecoderWrapper embeds the same row type and declares tagged fields of
// its own, so the promoted DecodeRow fills the embedded fields and the tags fill
// the declared ones.
type fieldedDecoderWrapper struct {
	selfDecodedUser
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

// ExportedDecodedUser is the same self-decoding row type under an exported name,
// which is what a generated row type looks like. An embedded field of an
// exported type is one the field path maps, where an embedded field of an
// unexported type is one it skips.
type ExportedDecodedUser struct {
	ID int64
}

func (u *ExportedDecodedUser) DecodeRow(r row.Dynamic) error {
	return row.Assign(r, "id", &u.ID)
}

// exportedFieldedDecoderWrapper embeds the exported row type and declares a
// tagged field of its own, so the field path maps the embedded field as well and
// looks for a column named after its type.
type exportedFieldedDecoderWrapper struct {
	ExportedDecodedUser
	Email string `rasql:"email"`
}

// skippedFieldedDecoderWrapper tags the embedded field out of the mapping, which
// is how such a wrapper states that only its own fields are mapped.
type skippedFieldedDecoderWrapper struct {
	ExportedDecodedUser `rasql:"-"`
	Email               string `rasql:"email"`
}

// Label is a column value a row type embeds rather than names, so the field it
// declares is anonymous and exported at once. The field path maps such a field
// like any other, so it is a field of the wrapper's own just as a named one is.
type Label string

// anonymousFieldedDecoderWrapper embeds the exported row type and one exported
// anonymous field of its own, tagged.
type anonymousFieldedDecoderWrapper struct {
	ExportedDecodedUser
	Label `rasql:"label"`
}

// untaggedAnonymousFieldedDecoderWrapper is that shape without a tag, so the
// field path names the column by snake-casing the field name.
type untaggedAnonymousFieldedDecoderWrapper struct {
	ExportedDecodedUser
	Label
}

// unexportedAnonymousFieldedDecoderWrapper embeds an unexported self-decoding row
// type, which the field path skips, so the anonymous field of its own is the only
// one it maps.
type unexportedAnonymousFieldedDecoderWrapper struct {
	selfDecodedUser
	Label `rasql:"label"`
}

// untaggedUnexportedAnonymousFieldedDecoderWrapper is that shape without a tag.
type untaggedUnexportedAnonymousFieldedDecoderWrapper struct {
	selfDecodedUser
	Label
}

// pointerFieldedDecoderWrapper embeds the row type by pointer, which promotes
// DecodeRow just as an embedded value does while leaving the pointer nil.
type pointerFieldedDecoderWrapper struct {
	*selfDecodedUser
	ID int64 `rasql:"id"`
}

// valueDecodingWrapper declares DecodeRow itself with a value receiver. Such a
// method cannot assign to the row, so it reports an error that names itself and
// the test observes that error rather than a decoded field.
type valueDecodingWrapper struct {
	selfDecodedUser
	ID int64 `rasql:"id"`
}

func (valueDecodingWrapper) DecodeRow(row.Dynamic) error {
	return errors.New("declared value receiver ran")
}

// pointerDecodingWrapper declares DecodeRow itself with a pointer receiver. Its
// tag names a different column than the method reads, so the decoded ID names
// which mapping ran.
type pointerDecodingWrapper struct {
	selfDecodedUser
	ID    int64 `rasql:"nickname"`
	Trace string
}

func (u *pointerDecodingWrapper) DecodeRow(r row.Dynamic) error {
	if err := row.Assign(r, "id", &u.ID); err != nil {
		return err
	}
	u.Trace = "declared"
	return nil
}

func TestDecodeIgnoresPromotedRowDecoder(t *testing.T) {
	result, err := row.New(
		[]string{"id", "email", "nickname", "label"},
		[]any{int64(42), []byte("ada@example.com"), int64(7), []byte("ada")},
	)
	require.NoError(t, err)

	t.Run("embedded value", func(t *testing.T) {
		decoded, err := row.Decode[fieldedDecoderWrapper](result)
		require.NoError(t, err)
		require.Equal(t, int64(42), decoded.ID)
		require.Equal(t, "ada@example.com", decoded.Email)
		// The promoted DecodeRow would have set Extra and filled the embedded
		// fields, so an empty embedded value proves the tags mapped the row.
		require.Equal(t, selfDecodedUser{}, decoded.selfDecodedUser)
	})

	t.Run("embedded pointer", func(t *testing.T) {
		decoded, err := row.Decode[pointerFieldedDecoderWrapper](result)
		require.NoError(t, err)
		require.Equal(t, int64(42), decoded.ID)
		require.Nil(t, decoded.selfDecodedUser)
	})

	t.Run("exported embedded field", func(t *testing.T) {
		// The field path maps an embedded field of an exported type like any
		// other field, and no column is named after its type, so the decode
		// fails rather than filling half of the row.
		_, err := row.Decode[exportedFieldedDecoderWrapper](result)
		require.ErrorContains(t, err, `row: column "exported_decoded_user" is not present`)
	})

	t.Run("exported embedded field tagged out", func(t *testing.T) {
		decoded, err := row.Decode[skippedFieldedDecoderWrapper](result)
		require.NoError(t, err)
		require.Equal(t, "ada@example.com", decoded.Email)
		require.Equal(t, ExportedDecodedUser{}, decoded.ExportedDecodedUser)
	})

	t.Run("tagged anonymous field", func(t *testing.T) {
		// The tagged anonymous Label is a mappable field of the wrapper, so the
		// wrapper takes the field path rather than the promoted DecodeRow. The
		// embedded row type is exported, so the field path maps it too and finds
		// no column named after it, which is what it does for a named field of an
		// exported type in the same shape.
		_, err := row.Decode[anonymousFieldedDecoderWrapper](result)
		require.ErrorContains(t, err, `row: column "exported_decoded_user" is not present`)
	})

	t.Run("untagged anonymous field", func(t *testing.T) {
		_, err := row.Decode[untaggedAnonymousFieldedDecoderWrapper](result)
		require.ErrorContains(t, err, `row: column "exported_decoded_user" is not present`)
	})

	t.Run("tagged anonymous field over an unexported decoder", func(t *testing.T) {
		// The field path skips the unexported embedded field and maps Label, so
		// the decode succeeds. The promoted DecodeRow would have filled the
		// embedded fields and left Label empty.
		decoded, err := row.Decode[unexportedAnonymousFieldedDecoderWrapper](result)
		require.NoError(t, err)
		require.Equal(t, Label("ada"), decoded.Label)
		require.Equal(t, selfDecodedUser{}, decoded.selfDecodedUser)
	})

	t.Run("untagged anonymous field over an unexported decoder", func(t *testing.T) {
		// Label carries no tag here, so the column is its snake-cased field name.
		decoded, err := row.Decode[untaggedUnexportedAnonymousFieldedDecoderWrapper](result)
		require.NoError(t, err)
		require.Equal(t, Label("ada"), decoded.Label)
		require.Equal(t, selfDecodedUser{}, decoded.selfDecodedUser)
	})
}

func TestDecodeFollowsDeclaredRowDecoder(t *testing.T) {
	result, err := row.New(
		[]string{"id", "email", "nickname"},
		[]any{int64(42), []byte("ada@example.com"), int64(7)},
	)
	require.NoError(t, err)

	t.Run("value receiver", func(t *testing.T) {
		_, err := row.Decode[valueDecodingWrapper](result)
		require.ErrorContains(t, err, "declared value receiver ran")
	})

	t.Run("pointer receiver", func(t *testing.T) {
		decoded, err := row.Decode[pointerDecodingWrapper](result)
		require.NoError(t, err)
		// The tag names "nickname", whose value is 7, so 42 proves the declared
		// method ran instead of the tag path.
		require.Equal(t, int64(42), decoded.ID)
		require.Equal(t, "declared", decoded.Trace)
	})
}

func TestNewRejectsInvalidShape(t *testing.T) {
	_, err := row.New([]string{"id"}, nil)
	require.Error(t, err)

	_, err = row.New([]string{"id", "id"}, []any{int64(1), int64(2)})
	require.Error(t, err)
}
