package engineprofile

import (
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestObservationVersionMatrix(t *testing.T) {
	for _, tc := range []struct {
		engine  EngineID
		raw     string
		version Version
		wantErr bool
	}{
		{PostgreSQL, "170000", Version{Known: true, Major: 17}, false},
		{PostgreSQL, "170006", Version{Known: true, Major: 17, Minor: 6}, false},
		{PostgreSQL, "160012", Version{Known: true, Major: 16, Minor: 12}, false},
		{PostgreSQL, "655530006", Version{}, true},
		{MySQL, "8.4.11-ubuntu", Version{Known: true, Major: 8, Minor: 4, Patch: 11}, false},
		{MySQL, "8.4.1.2", Version{}, true},
		{SQLite, "3.35.0", Version{Known: true, Major: 3, Minor: 35}, false},
		{SQLite, "3.35", Version{}, true},
	} {
		got, err := parseVersion(tc.engine, tc.raw)
		if tc.wantErr {
			require.Error(t, err)
			continue
		}
		require.NoError(t, err)
		require.Equal(t, tc.version, got)
	}
}

func TestResolveFailureIdentities(t *testing.T) {
	_, err := Resolve("missing", ObservedIdentity{Engine: PostgreSQL, Version: Version{Known: true, Major: 17}})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrUnknownProfile)
	var discovery *DiscoveryError
	require.True(t, errors.As(err, &discovery))
	require.Equal(t, ErrUnknownProfile, discovery.Code)
	_, err = Resolve("postgresql-17", ObservedIdentity{Engine: MySQL, Version: Version{Known: true, Major: 8, Minor: 4, Patch: 1}})
	require.ErrorIs(t, err, ErrProfileMismatch)
	require.True(t, errors.As(err, &discovery))
	require.Equal(t, ErrProfileMismatch, discovery.Code)
}

func TestProfileValidationMatrix(t *testing.T) {
	_, err := New("postgresql-17", PostgreSQL, "", Version{Known: false, Major: 17}, specs()[0].caps, Limits{65535})
	require.ErrorIs(t, err, ErrInvalidProfile)
	_, err = New("custom:custom", Custom, "custom", Version{Known: true, Major: 1}, Capabilities{}, Limits{1})
	require.NoError(t, err)
}
