package rasql_test

import (
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/stretchr/testify/require"
)

// graphCacheFuncDecoder carries a func field, which is the shape
// reflect.DeepEqual can never report equal: it calls two non-nil func values
// unequal even when they are the same function. A caller writes a decoder like
// this when decoding a column needs a conversion the decoder holds.
type graphCacheFuncDecoder struct {
	schema  rasql.ResultSchema
	convert func([]byte) []byte
}

// graphCacheIdentityPayload is a package-level function, so both decoders below
// hold the very same func value. Nothing about the two decoders differs.
func graphCacheIdentityPayload(value []byte) []byte { return value }

func (d graphCacheFuncDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (graphCacheFuncDecoder) Presence() []rasql.Presence         { return nil }
func (d graphCacheFuncDecoder) DecodeRow(source rasql.ScanSource, value *graphCacheChildRow) error {
	if err := source.Scan(&value.ID, &value.Parent, &value.Rank, &value.Payload); err != nil {
		return err
	}
	value.Payload = d.convert(value.Payload)
	return nil
}

// graphCacheFuncChildQuery builds the child query of graphCacheChildQuery with a
// func-bearing decoder in place of the plain one, so the two differ in nothing
// a cache key should see.
func graphCacheFuncChildQuery(t *testing.T, fixture graphCacheFixture, rank int64, codec string) rasql.Query[graphCacheChildRow] {
	t.Helper()
	projection, err := rasql.NewProjection(fixture.childItems,
		graphCacheFuncDecoder{schema: fixture.childSchema, convert: graphCacheIdentityPayload})
	require.NoError(t, err)
	query := rasql.Select(fixture.childSource, projection).
		OrderBy(rasql.AscExpr(fixture.childRank), rasql.AscExpr(fixture.childID))
	value, err := rasql.ValueWithCodec(rank, codec)
	require.NoError(t, err)
	return query.Where(rasql.EqualExpr(fixture.childRank, value))
}

func TestGraphCacheFuncBearingDecoder(t *testing.T) {
	t.Run("one plan node shares the cache when its decoder holds a func", func(t *testing.T) {
		fixture := graphCacheFixtureFor(t)
		codec := &graphCacheCodec{}
		executor := graphCacheExecutorWithCodec(t, fixture, codec)
		var mapped atomic.Int64
		// One plan, used by both edges, so both read the decoder out of the same
		// plan node and the two decoder values are the same value.
		shared := graphCacheChildPlan(t, graphCacheFuncChildQuery(t, fixture, 0, "graph.cache.fixed"), "shared", &mapped)
		plan := graphCacheParentPlan(t, fixture, shared, shared, rasql.EdgeOptions{BindLimit: 2})

		values, err := rasql.LoadGraph(t.Context(), executor, plan)
		require.NoError(t, err)
		require.Len(t, values, 2)

		// Two parents and a bind budget that fits one key per batch give the
		// first edge two statements; the second edge reads both batches out of
		// the cache. Four means every lookup missed, which is what
		// reflect.DeepEqual produces for a decoder holding a func.
		require.Equal(t, int64(2), fixture.counter.statements.Load())
		require.Equal(t, int64(2), codec.enc.Load())
		require.Equal(t, int64(4), mapped.Load())
		for _, value := range values {
			require.Len(t, value.First.Values, 1)
			require.Len(t, value.Second.Values, 1)
			require.Equal(t, "shared", value.First.Values[0].Marker)
			require.Equal(t, "shared", value.Second.Values[0].Marker)
		}
	})
}
