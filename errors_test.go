package rasql_test

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/exec"
	"github.com/stretchr/testify/require"
)

// TestRootSentinelsAreTheExecSentinels pins the one re-export in this package
// that a type alias cannot make automatic. ErrNoRows and ErrMultipleRows are
// variables, so root holds a second variable rather than a second name for the
// same one, and a caller that compares an error from rasql/dynamic against
// rasql.ErrNoRows is relying on the two holding the same value.
func TestRootSentinelsAreTheExecSentinels(t *testing.T) {
	// ErrNoRows wraps a zero-size struct value, not a pointer, so identity is
	// checked through equality rather than require.Same.
	require.Equal(t, exec.ErrNoRows, rasql.ErrNoRows)
	require.True(t, errors.Is(exec.ErrNoRows, rasql.ErrNoRows))
	require.True(t, errors.Is(rasql.ErrNoRows, sql.ErrNoRows))

	// ErrMultipleRows comes from errors.New, which returns a pointer, so
	// require.Same checks that root holds the very same value exec does.
	require.Same(t, exec.ErrMultipleRows, rasql.ErrMultipleRows)
	require.True(t, errors.Is(exec.ErrMultipleRows, rasql.ErrMultipleRows))
}
