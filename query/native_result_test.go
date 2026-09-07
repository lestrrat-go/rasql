package query_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

func TestNativeResultUsesAuthoritativeMetadataAndCopiesArgs(t *testing.T) {
	args := []any{"value"}
	body, err := query.NativeResultOf("sqlite", sqltext.Text("SELECT ?"), args)
	require.NoError(t, err)
	args[0] = "changed"
	got := body.Args()
	require.Equal(t, []any{"value"}, got)

	result, err := query.ResultOf(body, query.ResultColumn{Name: "value", Type: schema.IntegerType{}})
	require.NoError(t, err)
	require.Equal(t, body, result.Body())
}

func TestNativeResultRejectsDMLAndExecutableSemicolons(t *testing.T) {
	_, err := query.NativeResultOf("sqlite", sqltext.Text("UPDATE users SET name = ? RETURNING id"), []any{"Ada"})
	require.Error(t, err)
	_, err = query.NativeResultOf("sqlite", sqltext.Text("SELECT 1; SELECT 2"), nil)
	require.Error(t, err)
}
