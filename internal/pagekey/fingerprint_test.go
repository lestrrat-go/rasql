package pagekey_test

import (
	"math"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/pagekey"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestFingerprint(t *testing.T) {
	t.Run("preserves float bind bits", func(t *testing.T) {
		columns := []query.ResultColumn{{Name: "id", Type: schema.IntegerType{}}}
		keys := []pagekey.Field{{Direction: 1, Source: "items"}}
		positiveZero := stmt.New(sqltext.Text("SELECT id FROM items WHERE score = ?"), float64(0))
		negativeZero := stmt.New(sqltext.Text("SELECT id FROM items WHERE score = ?"), math.Copysign(0, -1))
		positiveNaN := stmt.New(sqltext.Text("SELECT id FROM items WHERE score = ?"), math.Float64frombits(0x7ff8000000000001))
		negativeNaN := stmt.New(sqltext.Text("SELECT id FROM items WHERE score = ?"), math.Float64frombits(0xfff8000000000001))

		positiveZeroFingerprint, err := pagekey.Fingerprint("sqlite", positiveZero.SQL(), columns, positiveZero, keys, []int{0})
		require.NoError(t, err)
		negativeZeroFingerprint, err := pagekey.Fingerprint("sqlite", negativeZero.SQL(), columns, negativeZero, keys, []int{0})
		require.NoError(t, err)
		positiveNaNFingerprint, err := pagekey.Fingerprint("sqlite", positiveNaN.SQL(), columns, positiveNaN, keys, []int{0})
		require.NoError(t, err)
		negativeNaNFingerprint, err := pagekey.Fingerprint("sqlite", negativeNaN.SQL(), columns, negativeNaN, keys, []int{0})
		require.NoError(t, err)

		require.NotEqual(t, positiveZeroFingerprint, negativeZeroFingerprint)
		require.NotEqual(t, positiveNaNFingerprint, negativeNaNFingerprint)
	})

	t.Run("changes with the filter but ignores cursor and limit binds", func(t *testing.T) {
		statement := stmt.New(sqltext.Text("SELECT id FROM items WHERE category = ? AND id > ? LIMIT ?"), "books", int64(7), int64(3))
		keys := []pagekey.Field{{Direction: 1, Source: "items"}}
		first, err := pagekey.Fingerprint("sqlite", "SELECT id FROM items WHERE category = ? ORDER BY id", []query.ResultColumn{{Name: "id", Type: schema.IntegerType{}}}, statement, keys, []int{0})
		require.NoError(t, err)
		changed := stmt.New(statement.Text(), "music", int64(7), int64(99))
		second, err := pagekey.Fingerprint("sqlite", "SELECT id FROM items WHERE category = ? ORDER BY id", []query.ResultColumn{{Name: "id", Type: schema.IntegerType{}}}, changed, keys, []int{0})
		require.NoError(t, err)
		require.NotEqual(t, first, second)
		require.False(t, strings.Contains(statement.SQL(), "cursor"))
	})

	t.Run("includes canonical column parameters", func(t *testing.T) {
		key := pagekey.Field{Direction: 1, Source: "items"}
		statement := stmt.New(sqltext.Text("SELECT id FROM items"))
		base := func(column schema.ColumnType) [32]byte {
			fingerprint, err := pagekey.Fingerprint("sqlite", statement.SQL(), []query.ResultColumn{{Name: "value", Type: column}}, statement, []pagekey.Field{key}, nil)
			require.NoError(t, err)
			return fingerprint
		}
		require.NotEqual(t, base(schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2)}), base(schema.DecimalType{Precision: 12, Scale: schema.NewDecimalScale(2)}))
		require.NotEqual(t, base(schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2)}), base(schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(3)}))
		require.NotEqual(t, base(schema.TextType{Width: schema.NewTextWidth(10)}), base(schema.TextType{Width: schema.NewTextWidth(20)}))
		require.NotEqual(t, base(schema.TextType{Width: schema.NewTextWidth(10)}), base(schema.TextType{Width: schema.NewTextWidth(10), Fixed: true}))
		require.NotEqual(t, base(schema.IntegerType{}), base(schema.IntegerType{Unsigned: true}))
		require.Equal(t, base(schema.IntegerType{}), base(schema.IntegerType{}))
	})
}
