package changeplan_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/stretchr/testify/require"
)

func TestCheckpointRoundTripAndRequiredFields(t *testing.T) {
	checkpoint, err := changeplan.NewCheckpoint(changeplan.PlanID{1}, 0, changeplan.Digest{2})
	require.NoError(t, err)
	encoded, err := changeplan.EncodeCheckpoint(checkpoint)
	require.NoError(t, err)
	decoded, err := changeplan.DecodeCheckpoint(encoded)
	require.NoError(t, err)
	require.Equal(t, checkpoint, decoded)
	for _, input := range []string{
		`{"plan_id":"0000000000000000000000000000000000000000000000000000000000000001","catalog_digest":"0000000000000000000000000000000000000000000000000000000000000002"}`,
		`{"plan_id":null,"next_index":0,"catalog_digest":"0000000000000000000000000000000000000000000000000000000000000002"}`,
		`{"plan_id":"0000000000000000000000000000000000000000000000000000000000000001","next_index":null,"catalog_digest":"0000000000000000000000000000000000000000000000000000000000000002"}`,
		`{"plan_id":"0000000000000000000000000000000000000000000000000000000000000001","next_index":0,"catalog_digest":"0000000000000000000000000000000000000000000000000000000000000002","extra":true}`,
	} {
		_, err := changeplan.DecodeCheckpoint([]byte(input))
		require.Error(t, err)
	}
}
