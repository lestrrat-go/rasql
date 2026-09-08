package external_test

import (
	"os"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/stretchr/testify/require"
)

func TestPublicProfileAndLockBoundary(t *testing.T) {
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	history, err := changeplan.NewHistoryIdentity("main", "schema_migrations")
	require.NoError(t, err)
	lockBytes, err := os.ReadFile("lock.json")
	require.NoError(t, err)
	digest, err := changeplan.ProfileDigest(profile)
	require.NoError(t, err)
	catalog, err := changeplan.NewCatalogIdentity(profile.Engine(), digest, changeplan.Digest{1}, changeplan.Digest{2})
	require.NoError(t, err)
	baseline, err := changeplan.NewBaselineIdentity(catalog, "fixtures/sqlite", nil, nil)
	require.NoError(t, err)
	plan, err := changeplan.NewPlan(profile, baseline, history, nil, nil)
	require.NoError(t, err)
	require.Equal(t, profile.ID(), plan.Profile().ID())
	_, err = changeplan.FromLock(lockBytes, profile, history, changeplan.ResolvedChanges{})
	require.Error(t, err)
	custom, err := rasql.NewCustomEngineProfile("acme", profile.Version(), profile.Capabilities(), profile.Limits())
	require.NoError(t, err)
	customDigest, err := changeplan.ProfileDigest(custom)
	require.NoError(t, err)
	require.NotEqual(t, digest, customDigest)
	_, err = rasql.NewCustomEngineProfile("", profile.Version(), profile.Capabilities(), profile.Limits())
	require.Error(t, err)
}
