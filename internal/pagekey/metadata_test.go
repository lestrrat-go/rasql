package pagekey_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/internal/pagekey"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

// A cursor written for ascending order must not be accepted by the same query
// ordered descending, so the direction has to reach the fingerprint.
func TestFingerprintSeparatesKeyMetadata(t *testing.T) {
	columns := []query.ResultColumn{{Name: "id", Type: schema.IntegerType{}}}
	statement := stmt.New(sqltext.Text("SELECT id FROM items"))
	of := func(f pagekey.Field) [32]byte {
		v, err := pagekey.Fingerprint("sqlite", statement.SQL(), columns, statement, []pagekey.Field{f}, nil)
		require.NoError(t, err)
		return v
	}
	base := pagekey.Field{Direction: 1, Source: "items"}
	require.NotEqual(t, of(base), of(pagekey.Field{Direction: 2, Source: "items"}), "direction")
	require.NotEqual(t, of(base), of(pagekey.Field{Direction: 1, Nulls: 2, Source: "items"}), "null order")
	require.NotEqual(t, of(base), of(pagekey.Field{Direction: 1, Codec: "x", Source: "items"}), "codec")
	require.NotEqual(t, of(base), of(pagekey.Field{Direction: 1, Source: "other"}), "source")
}
