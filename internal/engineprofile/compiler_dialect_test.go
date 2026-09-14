package engineprofile_test

import (
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/stretchr/testify/require"
)

func TestConstrainDialect(t *testing.T) {
	for _, tc := range []struct {
		profileID string
		base      dialect.Dialect
		version   engineprofile.Version
	}{
		{"postgresql-17", dialect.PostgreSQL(), engineprofile.Version{Known: true, Major: 17, Minor: 11}},
		{"mysql-8.4", dialect.MySQL(), engineprofile.Version{Known: true, Major: 8, Minor: 4, Patch: 11}},
		{"sqlite-3.35", dialect.SQLite(), engineprofile.Version{Known: true, Major: 3, Minor: 45, Patch: 1}},
	} {
		t.Run(tc.profileID, func(t *testing.T) {
			p, err := engineprofile.Builtin(tc.profileID, tc.version)
			require.NoError(t, err, `engineprofile.Builtin should succeed`)
			require.NoError(t, engineprofile.ValidateDialect(tc.base, p), `the built-in profile should agree with its dialect`)

			// A profile that validates constrains nothing, so a capability wired to the
			// wrong profile field shows up here as a dialect the profile disagrees with.
			constrained := engineprofile.ConstrainDialect(p, tc.base)
			require.Equal(t, tc.base.UpsertStyle(), constrained.UpsertStyle(), `upsert style should survive constraining`)
			for capability := dialect.Capability(1); capability != 0; capability <<= 1 {
				require.Equal(t, tc.base.Supports(capability), constrained.Supports(capability), `capability %d should survive constraining`, capability)
			}
		})
	}
}

func TestConstrainDialectWithdrawsCapability(t *testing.T) {
	base := dialect.PostgreSQL()
	require.True(t, base.Supports(dialect.CapabilityPartialIndex), `PostgreSQL should support partial indexes`)

	p, err := engineprofile.Builtin("postgresql-17", engineprofile.Version{Known: true, Major: 17, Minor: 11})
	require.NoError(t, err, `engineprofile.Builtin should succeed`)
	p.Capabilities.PartialIndex = false

	require.False(t, engineprofile.ConstrainDialect(p, base).Supports(dialect.CapabilityPartialIndex), `the profile should withdraw the capability`)

	err = engineprofile.ValidateDialect(base, p)
	var perr *engineprofile.ProfileError
	require.ErrorAs(t, err, &perr, `validation should fail with a profile error`)
	require.True(t, errors.Is(err, engineprofile.ErrInvalidProfile), `validation should report an invalid profile`)
	require.Equal(t, "partial index", perr.Feature, `validation should name the capability that disagrees`)
}
