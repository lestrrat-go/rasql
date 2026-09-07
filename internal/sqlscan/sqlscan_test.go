package sqlscan_test

import (
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/internal/sqlscan"
	"github.com/stretchr/testify/require"
)

func TestValidateSelectIgnoresProtectedSemicolonsAndPlaceholders(t *testing.T) {
	err := sqlscan.ValidateSelect("SELECT '?' AS literal, \";\" AS ident /* ?; */ -- $1;\n, $tag$;$tag$")
	require.NoError(t, err)
}

func TestValidateSelectRejectsExecutableSemicolon(t *testing.T) {
	err := sqlscan.ValidateSelect("SELECT 1; SELECT 2")
	require.Error(t, err)
}

func TestValidateSelectAcceptsSelectCTEAndRejectsDMLCTE(t *testing.T) {
	require.NoError(t, sqlscan.ValidateSelect("WITH values AS (SELECT 1) SELECT * FROM values"))
	err := sqlscan.ValidateSelect("WITH changed AS (DELETE FROM users RETURNING id) SELECT id FROM changed")
	require.Error(t, err)
	require.True(t, errors.Is(err, sqlscan.ErrNotSelect))
}

func TestScanRejectsUnterminatedProtectedRegions(t *testing.T) {
	for _, sql := range []string{"SELECT 'x", "SELECT /* x", "SELECT $tag$x"} {
		_, err := sqlscan.Scan(sql)
		require.Error(t, err, sql)
	}
}
