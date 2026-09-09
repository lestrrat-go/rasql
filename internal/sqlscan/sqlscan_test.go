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

func TestScanPreservesDollarPlaceholderKindAndOverflow(t *testing.T) {
	scan, err := sqlscan.Scan("SELECT $0, $999999999999999999999999")
	require.NoError(t, err)
	require.Len(t, scan.Placeholders, 2)
	require.Equal(t, sqlscan.Dollar, scan.Placeholders[0].Kind)
	require.Equal(t, 0, scan.Placeholders[0].Number)
	require.False(t, scan.Placeholders[0].Invalid)
	require.Equal(t, sqlscan.Dollar, scan.Placeholders[1].Kind)
	require.True(t, scan.Placeholders[1].Invalid)
}

func TestValidateSelectUsesEngineStringRules(t *testing.T) {
	for _, tc := range []struct {
		name   string
		engine string
		sql    string
	}{
		{name: "sqlite backslash is text", engine: "sqlite", sql: `SELECT '\' AS value, ?`},
		{name: "mysql backslash escapes", engine: "mysql", sql: "SELECT '\\\\' AS value, ?"},
		{name: "postgres escape string", engine: "postgresql", sql: "SELECT E'\\\\' AS value, $1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, sqlscan.ValidateSelect(tc.sql, tc.engine))
		})
	}
	_, err := sqlscan.Scan(`SELECT '\' AS value, ?`, "sqlite")
	require.NoError(t, err)
}

func TestValidateSelectChecksEveryCTEBody(t *testing.T) {
	for _, sql := range []string{
		"WITH RECURSIVE one AS (SELECT 1), two(v) AS (SELECT v FROM one) SELECT v FROM two",
		"WITH one AS (WITH two AS (SELECT 1) SELECT * FROM two) SELECT * FROM one",
		"WITH one AS NOT MATERIALIZED (SELECT 1) SELECT * FROM one",
	} {
		require.NoError(t, sqlscan.ValidateSelect(sql, "sqlite"), sql)
	}
	for _, sql := range []string{
		"WITH one(v) AS (VALUES (1)) SELECT v FROM one",
		"WITH one AS (TABLE users) SELECT * FROM one",
		"WITH one AS (DELETE FROM users RETURNING id) SELECT id FROM one",
		"WITH one AS (SELECT 1), two AS (UPDATE users SET id = 1 RETURNING id) SELECT id FROM two",
	} {
		err := sqlscan.ValidateSelect(sql, "postgresql")
		require.ErrorIs(t, err, sqlscan.ErrNotSelect, sql)
	}
}

func TestScanDoesNotApplyPostgresOrMySQLCommentsToOtherEngines(t *testing.T) {
	scan, err := sqlscan.Scan(`SELECT $tag$; ? $tag$, ?`, "sqlite")
	require.NoError(t, err)
	require.Empty(t, scan.Protected)
	require.Len(t, scan.Placeholders, 2)
	require.Len(t, scan.Semicolons, 1)

	scan, err = sqlscan.Scan("SELECT 1 --?x\n ?", "mysql")
	require.NoError(t, err)
	require.Len(t, scan.Placeholders, 2)

	scan, err = sqlscan.Scan("SELECT 1 # ?\n ?", "postgresql")
	require.NoError(t, err)
	require.Len(t, scan.Placeholders, 2)

	_, err = sqlscan.Scan(`SELECT E'\' AS value, ?`, "sqlite")
	require.NoError(t, err)
	_, err = sqlscan.Scan(`SELECT E'\' AS value, ?`, "postgresql")
	require.Error(t, err)
}
