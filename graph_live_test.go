//go:build unix

package rasql

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestPostgreSQLGraphLiveProfile(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	db, err := New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	profile, err := DiscoverEngineProfile(t.Context(), db, "postgresql-17")
	require.NoError(t, err)
	require.Equal(t, PostgreSQLEngine, profile.Engine())
	require.NotEqual(t, EnginePerParentLimitUnsupported, profile.Capabilities().PerParentLimit)
}

func TestMySQLGraphLiveProfile(t *testing.T) {
	database := dbtest.MySQLDB(t)
	db, err := New(database, dialect.MySQL())
	require.NoError(t, err)
	profile, err := DiscoverEngineProfile(t.Context(), db, "mysql-8.4")
	require.NoError(t, err)
	require.Equal(t, MySQLEngine, profile.Engine())
	require.NotEqual(t, EnginePerParentLimitUnsupported, profile.Capabilities().PerParentLimit)
}
