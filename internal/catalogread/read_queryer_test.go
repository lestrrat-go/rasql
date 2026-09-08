package catalogread_test

import (
	"context"
	"testing"

	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/stretchr/testify/require"
)

func TestReadQueryerEntryPointsValidateBeforeCallerState(t *testing.T) {
	profile := sqliteProfile(t)
	_, err := catalogread.ReadTx(context.Background(), nil, profile, catalogread.Scope{})
	require.EqualError(t, err, "catalog transaction must not be nil")

	_, err = catalogread.ReadConn(context.Background(), nil, profile, catalogread.Scope{})
	require.EqualError(t, err, "catalog connection must not be nil")

	custom, err := engineprofile.New("custom:test", engineprofile.Custom, "test", engineprofile.Version{}, engineprofile.Capabilities{}, engineprofile.Limits{MaxBindParameters: 1})
	require.NoError(t, err)
	_, err = catalogread.ReadTx(context.Background(), nil, custom, catalogread.Scope{})
	require.ErrorIs(t, err, engineprofile.ErrUnsupportedFeature)
	_, err = catalogread.ReadConn(context.Background(), nil, custom, catalogread.Scope{})
	require.ErrorIs(t, err, engineprofile.ErrUnsupportedFeature)
}

func TestReadQueryerNilCallerAndInspectorErrors(t *testing.T) {
	profile := sqliteProfile(t)

	_, err := catalogread.ReadTx(context.Background(), nil, profile, catalogread.Scope{})
	require.EqualError(t, err, "catalog transaction must not be nil")
	_, err = catalogread.ReadConn(context.Background(), nil, profile, catalogread.Scope{})
	require.EqualError(t, err, "catalog connection must not be nil")

	var invalid engineprofile.Profile
	_, err = catalogread.ReadTx(context.Background(), nil, invalid, catalogread.Scope{})
	require.Error(t, err)
}
