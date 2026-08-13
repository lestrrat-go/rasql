package rowvalue_test

import (
	"sync"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql/internal/rowvalue"
	"github.com/stretchr/testify/require"
)

func TestGetDecodesDriverValues(t *testing.T) {
	createdAt := time.Date(2026, time.August, 1, 12, 30, 0, 0, time.UTC)
	inputBytes := []byte("payload")
	result, err := rowvalue.NewRow(
		[]string{"active", "id", "ratio", "name", "payload", "created_at"},
		[]any{true, int64(42), 1.5, []byte("Ada"), inputBytes, createdAt},
	)
	require.NoError(t, err)

	gotActive, err := rowvalue.Get[bool](result, "active")
	require.NoError(t, err)
	require.True(t, gotActive)
	gotID, err := rowvalue.Get[int64](result, "id")
	require.NoError(t, err)
	require.Equal(t, int64(42), gotID)
	gotRatio, err := rowvalue.Get[float64](result, "ratio")
	require.NoError(t, err)
	require.Equal(t, 1.5, gotRatio)
	gotName, err := rowvalue.Get[string](result, "name")
	require.NoError(t, err)
	require.Equal(t, "Ada", gotName)
	gotPayload, err := rowvalue.Get[[]byte](result, "payload")
	require.NoError(t, err)
	require.Equal(t, []byte("payload"), gotPayload)
	gotWhen, err := rowvalue.Get[time.Time](result, "created_at")
	require.NoError(t, err)
	require.Equal(t, createdAt, gotWhen)

	inputBytes[0] = 'P'
	require.Equal(t, []byte("payload"), gotPayload)
}

func TestGetDecodesSQLiteValues(t *testing.T) {
	createdAt := time.Date(2026, time.August, 1, 12, 30, 0, 0, time.UTC)
	result, err := rowvalue.NewRow(
		[]string{"active", "created_at"},
		[]any{int64(1), createdAt.String()},
	)
	require.NoError(t, err)

	gotActive, err := rowvalue.Get[bool](result, "active")
	require.NoError(t, err)
	require.True(t, gotActive)
	gotWhen, err := rowvalue.Get[time.Time](result, "created_at")
	require.NoError(t, err)
	require.Equal(t, createdAt, gotWhen)
}

func TestGetRejectsWrongType(t *testing.T) {
	result, err := rowvalue.NewRow([]string{"id"}, []any{"42"})
	require.NoError(t, err)

	_, err = rowvalue.Get[int64](result, "id")
	require.Error(t, err)
}

func TestGetAndDecodePopulateTypedValues(t *testing.T) {
	result, err := rowvalue.NewRow(
		[]string{"id", "email", "nickname"},
		[]any{int64(42), []byte("ada@example.com"), nil},
	)
	require.NoError(t, err)

	id, err := rowvalue.Get[int64](result, "id")
	require.NoError(t, err)
	require.Equal(t, int64(42), id)

	type user struct {
		ID       int64
		Email    string
		Nickname *string
	}
	decoded, err := rowvalue.Decode[user](result)
	require.NoError(t, err)
	require.Equal(t, int64(42), decoded.ID)
	require.Equal(t, "ada@example.com", decoded.Email)
	require.Nil(t, decoded.Nickname)

	type summary struct {
		UserID int64 `rasql:"id"`
	}
	aliased, err := rowvalue.Decode[summary](result)
	require.NoError(t, err)
	require.Equal(t, int64(42), aliased.UserID)
}

func TestDecodeRejectsMissingColumnsAndUnsupportedDestinations(t *testing.T) {
	result, err := rowvalue.NewRow([]string{"id"}, []any{int64(42)})
	require.NoError(t, err)

	type user struct {
		Email string `rasql:"email"`
	}
	_, err = rowvalue.Decode[user](result)
	require.Error(t, err)
	_, err = rowvalue.Decode[string](result)
	require.Error(t, err)
}

func TestAssignDecodesIntoDestination(t *testing.T) {
	result, err := rowvalue.NewRow(
		[]string{"id", "email", "nickname"},
		[]any{int64(42), []byte("ada@example.com"), nil},
	)
	require.NoError(t, err)

	var id int64
	require.NoError(t, rowvalue.Assign(result, "id", &id))
	require.Equal(t, int64(42), id)

	var email string
	require.NoError(t, rowvalue.Assign(result, "email", &email))
	require.Equal(t, "ada@example.com", email)

	// A nullable column decodes into a nil pointer rather than failing.
	nickname := new(string)
	require.NoError(t, rowvalue.Assign(result, "nickname", &nickname))
	require.Nil(t, nickname)

	t.Run("missing column", func(t *testing.T) {
		var missing int64
		err := rowvalue.Assign(result, "nope", &missing)
		require.ErrorContains(t, err, `row: column "nope" is not present`)
	})

	t.Run("nil destination", func(t *testing.T) {
		var destination *int64
		err := rowvalue.Assign(result, "id", destination)
		require.ErrorContains(t, err, `row: destination for column "id" must not be nil`)
	})

	t.Run("conversion failure", func(t *testing.T) {
		var wrong bool
		err := rowvalue.Assign(result, "email", &wrong)
		require.ErrorContains(t, err, `row: decode column "email"`)
	})
}

// TestAssignDecodesBoolFromAnyNonzeroInteger pins MySQL's documented boolean
// semantics: TINYINT accepts the full signed range regardless of its display
// width, and any nonzero value (not just 1) is true. Both the text and binary
// driver protocols deliver int64 for a tiny field, and unsigned driver values
// must be covered too.
func TestAssignDecodesBoolFromAnyNonzeroInteger(t *testing.T) {
	result, err := rowvalue.NewRow(
		[]string{"zero", "two", "unsigned_two", "negative_one", "not_an_integer"},
		[]any{int64(0), int64(2), uint64(2), int64(-1), "true"},
	)
	require.NoError(t, err)

	var zero bool
	require.NoError(t, rowvalue.Assign(result, "zero", &zero))
	require.False(t, zero)

	var two bool
	require.NoError(t, rowvalue.Assign(result, "two", &two))
	require.True(t, two)

	var unsignedTwo bool
	require.NoError(t, rowvalue.Assign(result, "unsigned_two", &unsignedTwo))
	require.True(t, unsignedTwo)

	var negativeOne bool
	require.NoError(t, rowvalue.Assign(result, "negative_one", &negativeOne))
	require.True(t, negativeOne)

	var notAnInteger bool
	err = rowvalue.Assign(result, "not_an_integer", &notAnInteger)
	require.ErrorContains(t, err, `row: decode column "not_an_integer"`)
}

// TestAssignDecodesIntegersAcrossSignedness covers the uint64 field the
// generator emits for an unsigned integer column. Which signedness a driver
// delivers is the driver's choice rather than the column's:
// go-sql-driver/mysql hands back uint64 only for a BIGINT UNSIGNED column and
// int64 for a narrower unsigned one, so a uint64 field has to accept both. A
// value that does not fit the destination is an error, never a wrap.
func TestAssignDecodesIntegersAcrossSignedness(t *testing.T) {
	result, err := rowvalue.NewRow(
		[]string{"big_unsigned", "narrow_unsigned", "negative", "small_unsigned"},
		[]any{uint64(18446744073709551615), int64(42), int64(-1), uint64(7)},
	)
	require.NoError(t, err)

	t.Run("uint64 field takes an unsigned driver value", func(t *testing.T) {
		var destination uint64
		require.NoError(t, rowvalue.Assign(result, "big_unsigned", &destination))
		require.Equal(t, uint64(18446744073709551615), destination)
	})

	t.Run("uint64 field takes a signed driver value", func(t *testing.T) {
		var destination uint64
		require.NoError(t, rowvalue.Assign(result, "narrow_unsigned", &destination))
		require.Equal(t, uint64(42), destination)
	})

	t.Run("uint64 field rejects a negative value", func(t *testing.T) {
		var destination uint64
		err := rowvalue.Assign(result, "negative", &destination)
		require.ErrorContains(t, err, "-1 overflows uint64")
	})

	t.Run("int64 field takes a small unsigned value", func(t *testing.T) {
		var destination int64
		require.NoError(t, rowvalue.Assign(result, "small_unsigned", &destination))
		require.Equal(t, int64(7), destination)
	})

	// This is the range the whole change exists for: an int64 field cannot
	// hold what a BIGINT UNSIGNED column stores above 9223372036854775807, and
	// says so instead of wrapping to a negative number.
	t.Run("int64 field rejects a value above its range", func(t *testing.T) {
		var destination int64
		err := rowvalue.Assign(result, "big_unsigned", &destination)
		require.ErrorContains(t, err, "18446744073709551615 overflows int64")
	})
}

// TestAssignRejectsExactDecimalSourcesForFloat64 records why rasql maps NUMERIC
// and DECIMAL columns to a rejected inspection rather than schema.FloatType: the
// drivers hand back the exact decimal as a string or []byte, and a float64
// destination accepts neither, so the generated field could never decode it.
func TestAssignRejectsExactDecimalSourcesForFloat64(t *testing.T) {
	t.Run("string source", func(t *testing.T) {
		result, err := rowvalue.NewRow([]string{"pg_numeric"}, []any{"12.50"})
		require.NoError(t, err)

		var destination float64
		err = rowvalue.Assign(result, "pg_numeric", &destination)
		require.ErrorContains(t, err, `row: decode column "pg_numeric": expected float64, got string`)
	})

	t.Run("byte slice source", func(t *testing.T) {
		result, err := rowvalue.NewRow([]string{"mysql_decimal"}, []any{[]byte("12.50")})
		require.NoError(t, err)

		var destination float64
		err = rowvalue.Assign(result, "mysql_decimal", &destination)
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
		result, err := rowvalue.NewRow([]string{"pg_numeric"}, []any{"1234.5678901234567890"})
		require.NoError(t, err)

		var destination string
		require.NoError(t, rowvalue.Assign(result, "pg_numeric", &destination))
		require.Equal(t, "1234.5678901234567890", destination)
	})

	t.Run("byte slice source", func(t *testing.T) {
		result, err := rowvalue.NewRow([]string{"mysql_decimal"}, []any{[]byte("1234.5678901234567890")})
		require.NoError(t, err)

		var destination string
		require.NoError(t, rowvalue.Assign(result, "mysql_decimal", &destination))
		require.Equal(t, "1234.5678901234567890", destination)
	})
}

// embeddedUser is unexported, so the field walk skips it wherever it appears
// as an anonymous embedded field; only fields declared directly on the
// enclosing type are ever mapped through it.
type embeddedUser struct {
	ID    int64 `rasql:"nickname"`
	Email string
	Extra string
}

// fieldedWrapper embeds the unexported embeddedUser and declares tagged
// fields of its own. The field walk skips the embedded field, so only the
// declared ID and Email fields are mapped.
type fieldedWrapper struct {
	embeddedUser
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

// ExportedUser is exported, unlike embeddedUser, which is what a generated
// row type's exported name looks like. An embedded field of an exported type
// is one the field path maps, where an embedded field of an unexported type
// is one it skips.
type ExportedUser struct {
	ID int64
}

// exportedFieldedWrapper embeds the exported row type and declares a
// tagged field of its own, so the field path maps the embedded field as well and
// looks for a column named after its type.
type exportedFieldedWrapper struct {
	ExportedUser
	Email string `rasql:"email"`
}

// skippedFieldedWrapper tags the embedded field out of the mapping, which
// is how such a wrapper states that only its own fields are mapped.
type skippedFieldedWrapper struct {
	ExportedUser `rasql:"-"`
	Email        string `rasql:"email"`
}

// Label is a column value a row type embeds rather than names, so the field it
// declares is anonymous and exported at once. The field path maps such a field
// like any other, so it is a field of the wrapper's own just as a named one is.
type Label string

// anonymousFieldedWrapper embeds the exported row type and one exported
// anonymous field of its own, tagged.
type anonymousFieldedWrapper struct {
	ExportedUser
	Label `rasql:"label"`
}

// untaggedAnonymousFieldedWrapper is that shape without a tag, so the
// field path names the column by snake-casing the field name.
type untaggedAnonymousFieldedWrapper struct {
	ExportedUser
	Label
}

// unexportedAnonymousFieldedWrapper embeds the unexported embeddedUser,
// which the field path skips, so the anonymous field of its own is the only
// one it maps.
type unexportedAnonymousFieldedWrapper struct {
	embeddedUser
	Label `rasql:"label"`
}

// untaggedUnexportedAnonymousFieldedWrapper is that shape without a tag.
type untaggedUnexportedAnonymousFieldedWrapper struct {
	embeddedUser
	Label
}

// pointerFieldedWrapper embeds the row type by pointer. The field walk skips
// it the same way it skips an embedded value, leaving the pointer nil.
type pointerFieldedWrapper struct {
	*embeddedUser
	ID int64 `rasql:"id"`
}

// decodeFromBothOrders runs check against two Dynamic values holding the same
// data under different column orders, so a cached shadow decision that turned
// out to depend on which row built the cache first would show up as a result
// that differs between the two calls. This is what proves the cached decision
// is a property of the type rather than of whichever row reached it first.
func decodeFromBothOrders(t *testing.T, forward, reordered rowvalue.Row, check func(t *testing.T, source rowvalue.Row)) {
	t.Helper()
	t.Run("forward column order", func(t *testing.T) { check(t, forward) })
	t.Run("reordered columns", func(t *testing.T) { check(t, reordered) })
}

func TestDecodeMapsEmbeddedFieldsByName(t *testing.T) {
	result, err := rowvalue.NewRow(
		[]string{"id", "email", "nickname", "label"},
		[]any{int64(42), []byte("ada@example.com"), int64(7), []byte("ada")},
	)
	require.NoError(t, err)
	reordered, err := rowvalue.NewRow(
		[]string{"label", "nickname", "email", "id"},
		[]any{[]byte("ada"), int64(7), []byte("ada@example.com"), int64(42)},
	)
	require.NoError(t, err)

	t.Run("embedded value", func(t *testing.T) {
		decodeFromBothOrders(t, result, reordered, func(t *testing.T, source rowvalue.Row) {
			decoded, err := rowvalue.Decode[fieldedWrapper](source)
			require.NoError(t, err)
			require.Equal(t, int64(42), decoded.ID)
			require.Equal(t, "ada@example.com", decoded.Email)
			// embeddedUser is unexported, so the field walk skips it entirely; an
			// empty embedded value proves only the declared tags mapped the row.
			require.Equal(t, embeddedUser{}, decoded.embeddedUser)
		})
	})

	t.Run("embedded pointer", func(t *testing.T) {
		decodeFromBothOrders(t, result, reordered, func(t *testing.T, source rowvalue.Row) {
			decoded, err := rowvalue.Decode[pointerFieldedWrapper](source)
			require.NoError(t, err)
			require.Equal(t, int64(42), decoded.ID)
			require.Nil(t, decoded.embeddedUser)
		})
	})

	t.Run("exported embedded field", func(t *testing.T) {
		// The field path maps an embedded field of an exported type like any
		// other field, and no column is named after its type, so the decode
		// fails rather than filling half of the row.
		decodeFromBothOrders(t, result, reordered, func(t *testing.T, source rowvalue.Row) {
			_, err := rowvalue.Decode[exportedFieldedWrapper](source)
			require.ErrorContains(t, err, `row: column "exported_user" is not present`)
		})
	})

	t.Run("exported embedded field tagged out", func(t *testing.T) {
		decodeFromBothOrders(t, result, reordered, func(t *testing.T, source rowvalue.Row) {
			decoded, err := rowvalue.Decode[skippedFieldedWrapper](source)
			require.NoError(t, err)
			require.Equal(t, "ada@example.com", decoded.Email)
			require.Equal(t, ExportedUser{}, decoded.ExportedUser)
		})
	})

	t.Run("tagged anonymous field", func(t *testing.T) {
		// The tagged anonymous Label is a mappable field of the wrapper. The
		// embedded row type is exported, so the field path maps it too and finds
		// no column named after it, which is what it does for a named field of an
		// exported type in the same shape.
		decodeFromBothOrders(t, result, reordered, func(t *testing.T, source rowvalue.Row) {
			_, err := rowvalue.Decode[anonymousFieldedWrapper](source)
			require.ErrorContains(t, err, `row: column "exported_user" is not present`)
		})
	})

	t.Run("untagged anonymous field", func(t *testing.T) {
		decodeFromBothOrders(t, result, reordered, func(t *testing.T, source rowvalue.Row) {
			_, err := rowvalue.Decode[untaggedAnonymousFieldedWrapper](source)
			require.ErrorContains(t, err, `row: column "exported_user" is not present`)
		})
	})

	t.Run("tagged anonymous field over an unexported embedded type", func(t *testing.T) {
		// The field path skips the unexported embedded field and maps Label, so
		// the decode succeeds.
		decodeFromBothOrders(t, result, reordered, func(t *testing.T, source rowvalue.Row) {
			decoded, err := rowvalue.Decode[unexportedAnonymousFieldedWrapper](source)
			require.NoError(t, err)
			require.Equal(t, Label("ada"), decoded.Label)
			require.Equal(t, embeddedUser{}, decoded.embeddedUser)
		})
	})

	t.Run("untagged anonymous field over an unexported embedded type", func(t *testing.T) {
		// Label carries no tag here, so the column is its snake-cased field name.
		decodeFromBothOrders(t, result, reordered, func(t *testing.T, source rowvalue.Row) {
			decoded, err := rowvalue.Decode[untaggedUnexportedAnonymousFieldedWrapper](source)
			require.NoError(t, err)
			require.Equal(t, Label("ada"), decoded.Label)
			require.Equal(t, embeddedUser{}, decoded.embeddedUser)
		})
	})
}

func TestNewRejectsInvalidShape(t *testing.T) {
	_, err := rowvalue.NewRow([]string{"id"}, nil)
	require.Error(t, err)

	_, err = rowvalue.NewRow([]string{"id", "id"}, []any{int64(1), int64(2)})
	require.Error(t, err)
}

// TestDecodeErrorsFromFieldMappingRules covers the three field-mapping error
// cases buildPlan now resolves once per type instead of Decode recomputing on
// every row: none of them had a dedicated test before this change.
func TestDecodeErrorsFromFieldMappingRules(t *testing.T) {
	result, err := rowvalue.NewRow([]string{"id"}, []any{int64(42)})
	require.NoError(t, err)

	t.Run("unexported field with a tag is not exported", func(t *testing.T) {
		type badRow struct {
			hidden int64 `rasql:"id"`
		}
		_, err := rowvalue.Decode[badRow](result)
		require.ErrorContains(t, err, `row: field hidden for column "id" is not exported`)
		_ = badRow{}.hidden // silence unused-field vet noise from the tag-only reference
	})

	t.Run("tag with an empty column name", func(t *testing.T) {
		type badRow struct {
			ID int64 `rasql:""`
		}
		_, err := rowvalue.Decode[badRow](result)
		require.ErrorContains(t, err, "row: field ID has an empty rasql column name")
	})

	t.Run("no mappable fields at all", func(t *testing.T) {
		type badRow struct {
			hidden int64
		}
		_, err := rowvalue.Decode[badRow](result)
		require.ErrorContains(t, err, "has no exported fields")
		_ = badRow{}.hidden
	})

	t.Run("non-struct destination", func(t *testing.T) {
		_, err := rowvalue.Decode[int64](result)
		require.ErrorContains(t, err, "row: decode destination int64 must be a struct")
	})
}

// TestNewDynamicIsIndependentOfCallerSlices proves NewDynamic still hands back
// a row the caller cannot reach through its own slices afterward, which the
// map representation gave for free and the header/values representation has
// to earn explicitly by cloning both the names and the values.
func TestNewDynamicIsIndependentOfCallerSlices(t *testing.T) {
	names := []string{"id", "payload"}
	payload := []byte("original")
	values := []any{int64(1), payload}

	result, err := rowvalue.NewRow(names, values)
	require.NoError(t, err)

	names[0] = "mutated"
	values[0] = int64(999)
	payload[0] = 'X'

	id, err := rowvalue.Get[int64](result, "id")
	require.NoError(t, err)
	require.Equal(t, int64(1), id)

	stored, err := rowvalue.Get[[]byte](result, "payload")
	require.NoError(t, err)
	require.Equal(t, []byte("original"), stored)
}

// TestZeroDynamicIsSafe proves every read path treats a zero-value Dynamic
// the way it always treated a nil map: every column reports absent rather
// than panicking.
func TestZeroDynamicIsSafe(t *testing.T) {
	var zero rowvalue.Row

	var destination int64
	err := rowvalue.Assign(zero, "id", &destination)
	require.ErrorContains(t, err, `row: column "id" is not present`)

	_, err = rowvalue.Get[int64](zero, "id")
	require.ErrorContains(t, err, `row: column "id" is not present`)

	type user struct {
		ID int64
	}
	_, err = rowvalue.Decode[user](zero)
	require.ErrorContains(t, err, `row: column "id" is not present`)
}

// concurrentPlanRowA and concurrentPlanRowB are two distinct row types so that
// decoding them from two goroutines at once exercises decodePlans, the
// package-level sync.Map, on two different keys concurrently.
type concurrentPlanRowA struct {
	ID int64
}

type concurrentPlanRowB struct {
	Name string
}

// TestDecodeConcurrentTypesAreSafe decodes two different row types from
// Dynamic values of the same query in two goroutines. Run under -race, it
// exercises the plan cache's sync.Map and the columnHeader two Dynamic values
// of the query would share, proving both are safe for concurrent readers.
func TestDecodeConcurrentTypesAreSafe(t *testing.T) {
	result, err := rowvalue.NewRow([]string{"id", "name"}, []any{int64(42), "Ada"})
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(2)
	var aErr, bErr error
	go func() {
		defer wg.Done()
		for range 200 {
			_, aErr = rowvalue.Decode[concurrentPlanRowA](result)
			if aErr != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			_, bErr = rowvalue.Decode[concurrentPlanRowB](result)
			if bErr != nil {
				return
			}
		}
	}()
	wg.Wait()
	require.NoError(t, aErr)
	require.NoError(t, bErr)
}

// orderedErrorRow declares a field whose column is missing before a field
// whose tag is invalid. Before the plan cache, Decode recomputed both facts
// while walking fields row by row, so the first field's error -- the missing
// column -- won because the loop reached it first. The plan is now resolved
// for the whole type once, before any row is read, so the field-mapping
// error always wins even though it is declared second.
type orderedErrorRow struct {
	Missing int64
	Invalid int64 `rasql:""`
}

func TestDecodeErrorOrderPrefersPlanError(t *testing.T) {
	result, err := rowvalue.NewRow([]string{"id"}, []any{int64(42)})
	require.NoError(t, err)

	_, err = rowvalue.Decode[orderedErrorRow](result)
	require.ErrorContains(t, err, "row: field Invalid has an empty rasql column name")
	require.NotContains(t, err.Error(), "Missing")
}
