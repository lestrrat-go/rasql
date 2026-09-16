package rasql_test

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/stretchr/testify/require"
)

// TestErrNoRowsUnwrapsToStandardSentinel pins that ErrNoRows stays
// recognizable to code that already branches on database/sql's own sentinel,
// even though ErrNoRows carries a message of its own.
func TestErrNoRowsUnwrapsToStandardSentinel(t *testing.T) {
	require.True(t, errors.Is(rasql.ErrNoRows, sql.ErrNoRows))
}
