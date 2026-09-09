package rasql

import (
	"math"
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestPageFingerprintPreservesFloatBindBits(t *testing.T) {
	statementSchema := mustRuntimeSchema(t, ResultColumn{Name: "id", Type: schema.IntegerType{}})
	keys := []*pageKey[int]{{direction: PageAscending, term: OrderTerm{source: "items"}}}
	positiveZero := stmt.New(sqltext.Text("SELECT id FROM items WHERE score = ?"), float64(0))
	negativeZero := stmt.New(sqltext.Text("SELECT id FROM items WHERE score = ?"), math.Copysign(0, -1))
	positiveNaN := stmt.New(sqltext.Text("SELECT id FROM items WHERE score = ?"), math.Float64frombits(0x7ff8000000000001))
	negativeNaN := stmt.New(sqltext.Text("SELECT id FROM items WHERE score = ?"), math.Float64frombits(0xfff8000000000001))

	positiveZeroFingerprint, err := pageFingerprint("sqlite", positiveZero.SQL(), statementSchema, positiveZero, keys, []int{0})
	require.NoError(t, err)
	negativeZeroFingerprint, err := pageFingerprint("sqlite", negativeZero.SQL(), statementSchema, negativeZero, keys, []int{0})
	require.NoError(t, err)
	positiveNaNFingerprint, err := pageFingerprint("sqlite", positiveNaN.SQL(), statementSchema, positiveNaN, keys, []int{0})
	require.NoError(t, err)
	negativeNaNFingerprint, err := pageFingerprint("sqlite", negativeNaN.SQL(), statementSchema, negativeNaN, keys, []int{0})
	require.NoError(t, err)

	require.NotEqual(t, positiveZeroFingerprint, negativeZeroFingerprint)
	require.NotEqual(t, positiveNaNFingerprint, negativeNaNFingerprint)
}
