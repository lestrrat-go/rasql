package rasqlgen

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTypedSQLOnlineOfflineLoweringParity(t *testing.T) {
	source := `SELECT id FROM users WHERE id = {{bind "id"}} OR parent_id = {{bind "id"}} AND name = {{bind "name"}}`
	onlineSQL, onlineArgs, err := lowerTypedSQL(source, "users", "sqlite")
	require.NoError(t, err)
	offlineSQL, offlineArgs, err := lowerTypedSQL(source, "users", "sqlite")
	require.NoError(t, err)
	require.Equal(t, onlineSQL, offlineSQL)
	require.Equal(t, onlineArgs, offlineArgs)
	changedSQL, changedArgs, err := lowerTypedSQL(source+" ", "users", "sqlite")
	require.NoError(t, err)
	require.NotEqual(t, onlineSQL, changedSQL)
	require.Equal(t, onlineArgs, changedArgs)
}
