package row_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql/internal/rowvalue"
	"github.com/lestrrat-go/rasql/row"
	"github.com/stretchr/testify/require"
)

var (
	_ row.ScanSource = (*sql.Row)(nil)
	_ row.ScanSource = (*sql.Rows)(nil)
)

// Dynamic must be an alias for rowvalue.Row, not a defined type over it:
// every signature in the root package names row.Dynamic, and a defined type
// would need a conversion at each one.
var _ = func(r rowvalue.Row) row.Dynamic { return r }

// TestForwardersPassArgumentsThrough proves each row/ forwarder passes its
// arguments to internal/rowvalue in the right order, rather than duplicating
// the behavior suite that now lives in internal/rowvalue.
func TestForwardersPassArgumentsThrough(t *testing.T) {
	createdAt := time.Date(2026, time.August, 1, 12, 30, 0, 0, time.UTC)
	result, err := row.NewDynamic(
		[]string{"id", "created_at"},
		[]any{int64(42), createdAt},
	)
	require.NoError(t, err)

	id, err := row.Get[int64](result, "id")
	require.NoError(t, err)
	require.Equal(t, int64(42), id)

	var destination int64
	require.NoError(t, row.Assign(result, "id", &destination))
	require.Equal(t, int64(42), destination)

	type target struct {
		ID        int64
		CreatedAt time.Time
	}
	decoded, err := row.Decode[target](result)
	require.NoError(t, err)
	require.Equal(t, int64(42), decoded.ID)
	require.Equal(t, createdAt, decoded.CreatedAt)

	_, err = row.Get[int64](result, "missing")
	require.ErrorContains(t, err, `row: column "missing" is not present`)
}
