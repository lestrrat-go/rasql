package rowvalue_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/internal/rowvalue"
	"github.com/stretchr/testify/require"
)

// planOnceFieldedRow is declared only in this test, so its reflect.Type is
// used by no other test in the package and a plan build can be attributed
// to it alone.
type planOnceFieldedRow struct {
	ID int64
}

// planOnceEmbeddedRow is unexported, so the field-mapping walk skips the
// anonymous field it backs and planOnceEmbedWithFields is field-mapped only
// through the field it declares of its own.
type planOnceEmbeddedRow struct {
	ID int64
}

// planOnceEmbedWithFields embeds an unexported row type and declares a field
// of its own, so building its plan exercises the field walk across an
// anonymous, unexported field.
type planOnceEmbedWithFields struct {
	planOnceEmbeddedRow
	Extra int64 `rasql:"extra"`
}

// planOnceEmptyTag carries a rasql tag with an empty column name, which
// buildPlan turns into a permanent per-type error.
type planOnceEmptyTag struct {
	Name string `rasql:""`
}

type decoderValidationRow struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

func TestNewDecoderValidatesStructureAndColumns(t *testing.T) {
	_, err := rowvalue.NewDecoder[int]()
	require.EqualError(t, err, "row: decode destination int must be a struct")

	type empty struct{}
	_, err = rowvalue.NewDecoder[empty]()
	require.EqualError(t, err, "row: decode destination rowvalue_test.empty has no exported fields")

	decoder, err := rowvalue.NewDecoder[decoderValidationRow]()
	require.NoError(t, err)
	require.NoError(t, decoder.ValidateColumns([]string{"id", "email", "extra"}))
	require.EqualError(t, decoder.ValidateColumns([]string{"id"}), `row: column "email" is not present`)

	row, err := rowvalue.NewRow([]string{"id", "email"}, []any{int64(7), "ada@example.com"})
	require.NoError(t, err)
	decoded, err := decoder.Decode(row)
	require.NoError(t, err)
	require.Equal(t, decoderValidationRow{ID: 7, Email: "ada@example.com"}, decoded)
}

// TestPlanIsBuiltOncePerType decodes each row type many times over and checks
// that decodePlanBuilds advances by exactly one per type, proving planFor
// caches a plan rather than rebuilding it on every Decode call.
func TestPlanIsBuiltOncePerType(t *testing.T) {
	t.Run("field-mapped struct", func(t *testing.T) {
		source, err := rowvalue.NewRow([]string{"id"}, []any{int64(42)})
		require.NoError(t, err)

		before := rowvalue.PlanBuildCount()
		for range 100 {
			decoded, err := rowvalue.Decode[planOnceFieldedRow](source)
			require.NoError(t, err)
			require.Equal(t, int64(42), decoded.ID)
		}
		require.Equal(t, int64(1), rowvalue.PlanBuildCount()-before)
	})

	t.Run("struct embedding an unexported row type with fields of its own", func(t *testing.T) {
		source, err := rowvalue.NewRow([]string{"extra"}, []any{int64(7)})
		require.NoError(t, err)

		before := rowvalue.PlanBuildCount()
		for range 100 {
			decoded, err := rowvalue.Decode[planOnceEmbedWithFields](source)
			require.NoError(t, err)
			require.Equal(t, int64(7), decoded.Extra)
			// planOnceEmbeddedRow is unexported, so the field walk skips its
			// anonymous field entirely and ID stays zero.
			require.Zero(t, decoded.ID)
		}
		require.Equal(t, int64(1), rowvalue.PlanBuildCount()-before)
	})

	t.Run("struct whose plan is an error", func(t *testing.T) {
		source, err := rowvalue.NewRow([]string{"name"}, []any{"Ada"})
		require.NoError(t, err)

		before := rowvalue.PlanBuildCount()
		_, firstErr := rowvalue.Decode[planOnceEmptyTag](source)
		_, secondErr := rowvalue.Decode[planOnceEmptyTag](source)
		require.Error(t, firstErr)
		require.EqualError(t, secondErr, firstErr.Error())
		require.Equal(t, int64(1), rowvalue.PlanBuildCount()-before)
	})
}
