package rasql_test

import (
	"github.com/lestrrat-go/rasql"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPublicEngineProfileAccessorsAndOpaqueCustomValues(t *testing.T) {
	p, err := rasql.EngineProfileFromVersion("postgresql-17", 17, 6, 0)
	require.NoError(t, err)
	require.Equal(t, "postgresql-17", p.ID())
	require.Equal(t, rasql.PostgreSQLEngine, p.Engine())
	require.Equal(t, uint16(17), p.Version().Major)
	custom, err := rasql.NewCustomEngineProfile("adapter", rasql.EngineVersion{Known: true, Major: 1}, rasql.EngineCapabilities{}, rasql.EngineLimits{MaxBindParameters: 10})
	require.NoError(t, err)
	require.Equal(t, rasql.CustomEngine, custom.Engine())
	require.Equal(t, "custom:adapter", custom.ID())
	require.Equal(t, 10, custom.Limits().MaxBindParameters)
}

func TestPublicEngineProfileRejectsUnknownAndOverflow(t *testing.T) {
	_, err := rasql.EngineProfileFromVersion("unknown", 1, 0, 0)
	require.ErrorIs(t, err, rasql.ErrUnknownEngineProfile)
	_, err = rasql.EngineProfileFromVersion("postgresql-17", 70000, 0, 0)
	require.ErrorIs(t, err, rasql.ErrInvalidEngineProfile)
}
