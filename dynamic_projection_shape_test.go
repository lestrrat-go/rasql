package rasql

import (
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestDynamicProjectionRejectsNonStructResultType covers a gap the
// returning_preflight_test.go audit found: internal/rowvalue.Decoder used to
// refuse a non-struct decode destination with "row: decode destination %T
// must be a struct", but that decoder is dead code today (NewDecoder has no
// caller left in this module now that QueryWriteAll and QueryWriteOne are
// gone). DynamicProjection is the canonical replacement, its dynamicFields
// check carries the same restriction under a different message, and it is
// reachable through any caller building a Projection[R] for a non-struct R
// with DynamicProjection instead of a hand-written RowDecoder[R] -- but
// nothing in dynamic_projection_test.go called it with one, so this check
// had no test at all until this one.
func TestDynamicProjectionRejectsNonStructResultType(t *testing.T) {
	result, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	_, err = DynamicProjection[int64](result)
	require.ErrorContains(t, err, "must be a struct")
}
