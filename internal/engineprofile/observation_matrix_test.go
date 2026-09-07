package engineprofile

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseVersionBoundaryAndMalformedMatrix(t *testing.T) {
	tests := []struct {
		name    string
		engine  EngineID
		raw     string
		want    Version
		wantErr bool
	}{
		{name: "postgres minimum", engine: PostgreSQL, raw: "100000", want: Version{Known: true, Major: 10}},
		{name: "postgres example", engine: PostgreSQL, raw: "170006", want: Version{Known: true, Major: 17, Minor: 6}},
		{name: "postgres maximum", engine: PostgreSQL, raw: "655359999", want: Version{Known: true, Major: 65535, Minor: 9999}},
		{name: "postgres below minimum", engine: PostgreSQL, raw: "99999", wantErr: true},
		{name: "postgres non decimal", engine: PostgreSQL, raw: "17.6", wantErr: true},
		{name: "postgres overflow", engine: PostgreSQL, raw: "18446744073709551616", wantErr: true},
		{name: "mysql minimum", engine: MySQL, raw: "8.4.0", want: Version{Known: true, Major: 8, Minor: 4}},
		{name: "mysql suffix", engine: MySQL, raw: "8.4.11-ubuntu", want: Version{Known: true, Major: 8, Minor: 4, Patch: 11}},
		{name: "mysql plus suffix", engine: MySQL, raw: "8.4.11+vendor", want: Version{Known: true, Major: 8, Minor: 4, Patch: 11}},
		{name: "mysql missing patch", engine: MySQL, raw: "8.4", wantErr: true},
		{name: "mysql extra component", engine: MySQL, raw: "8.4.1.2", wantErr: true},
		{name: "mysql trailing text", engine: MySQL, raw: "8.4.1vendor", wantErr: true},
		{name: "mysql negative", engine: MySQL, raw: "8.-4.1", wantErr: true},
		{name: "mysql overflow", engine: MySQL, raw: "8.4.65536", wantErr: true},
		{name: "sqlite minimum", engine: SQLite, raw: "3.35.0", want: Version{Known: true, Major: 3, Minor: 35}},
		{name: "sqlite maximum", engine: SQLite, raw: "3.65535.65535", want: Version{Known: true, Major: 3, Minor: 65535, Patch: 65535}},
		{name: "sqlite missing component", engine: SQLite, raw: "3.35", wantErr: true},
		{name: "sqlite suffix", engine: SQLite, raw: "3.35.0-ubuntu", wantErr: true},
		{name: "sqlite extra component", engine: SQLite, raw: "3.35.0.1", wantErr: true},
		{name: "sqlite negative", engine: SQLite, raw: "3.-35.0", wantErr: true},
		{name: "sqlite overflow", engine: SQLite, raw: "3.35.65536", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseVersion(tc.engine, tc.raw)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestResolveCompleteProfileBoundaryMatrix(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		engine  EngineID
		version Version
		wantErr error
	}{
		{name: "postgres16 lower", id: "postgresql-16", engine: PostgreSQL, version: Version{Known: true, Major: 16}},
		{name: "postgres16 upper", id: "postgresql-16", engine: PostgreSQL, version: Version{Known: true, Major: 16, Minor: 65535}},
		{name: "postgres16 major below", id: "postgresql-16", engine: PostgreSQL, version: Version{Known: true, Major: 15, Minor: 65535, Patch: 65535}, wantErr: ErrUnsupportedVersion},
		{name: "postgres17 lower", id: "postgresql-17", engine: PostgreSQL, version: Version{Known: true, Major: 17}},
		{name: "postgres17 upper", id: "postgresql-17", engine: PostgreSQL, version: Version{Known: true, Major: 17, Minor: 65535}},
		{name: "mysql lower", id: "mysql-8.4", engine: MySQL, version: Version{Known: true, Major: 8, Minor: 4}},
		{name: "mysql upper", id: "mysql-8.4", engine: MySQL, version: Version{Known: true, Major: 8, Minor: 4, Patch: 65535}},
		{name: "mysql minor below", id: "mysql-8.4", engine: MySQL, version: Version{Known: true, Major: 8, Minor: 3}, wantErr: ErrUnsupportedVersion},
		{name: "mysql minor above", id: "mysql-8.4", engine: MySQL, version: Version{Known: true, Major: 8, Minor: 5}, wantErr: ErrUnsupportedVersion},
		{name: "sqlite lower", id: "sqlite-3.35", engine: SQLite, version: Version{Known: true, Major: 3, Minor: 35}},
		{name: "sqlite upper", id: "sqlite-3.35", engine: SQLite, version: Version{Known: true, Major: 3, Minor: 65535, Patch: 65535}},
		{name: "sqlite minor below", id: "sqlite-3.35", engine: SQLite, version: Version{Known: true, Major: 3, Minor: 34}, wantErr: ErrUnsupportedVersion},
		{name: "sqlite patch upper boundary", id: "sqlite-3.35", engine: SQLite, version: Version{Known: true, Major: 3, Minor: 35, Patch: 65535}, wantErr: nil},
		{name: "unknown observed version", id: "postgresql-17", engine: PostgreSQL, version: Version{}, wantErr: ErrUnsupportedVersion},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(tc.id, ObservedIdentity{Engine: tc.engine, Version: tc.version})
			if tc.wantErr != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.NotEmpty(t, got.ID)
		})
	}
	_, err := Resolve("postgresql-17", ObservedIdentity{Engine: MySQL, Version: Version{Known: true, Major: 8, Minor: 4}})
	require.ErrorIs(t, err, ErrProfileMismatch)
	_, err = Resolve("missing", ObservedIdentity{Engine: PostgreSQL, Version: Version{Known: true, Major: 17}})
	require.ErrorIs(t, err, ErrUnknownProfile)
}

func TestProfileValidationExactCases(t *testing.T) {
	pg := specs()[1]
	tests := []struct {
		name string
		make func() (Profile, error)
	}{
		{name: "zero engine", make: func() (Profile, error) {
			return New("x", 0, "", Version{Known: true, Major: 1}, Capabilities{}, Limits{1})
		}},
		{name: "zero bind limit", make: func() (Profile, error) { return New("x", Custom, "custom", Version{}, Capabilities{}, Limits{}) }},
		{name: "unknown version numbers", make: func() (Profile, error) {
			return New("x", Custom, "custom", Version{Major: 1}, Capabilities{}, Limits{1})
		}},
		{name: "known zero major", make: func() (Profile, error) {
			return New("x", Custom, "custom", Version{Known: true}, Capabilities{}, Limits{1})
		}},
		{name: "custom name missing", make: func() (Profile, error) { return New("x", Custom, "", Version{}, Capabilities{}, Limits{1}) }},
		{name: "builtin custom name", make: func() (Profile, error) {
			return New(pg.id, PostgreSQL, "custom", Version{Known: true, Major: 17}, pg.caps, Limits{pg.binds})
		}},
		{name: "builtin wrong bind", make: func() (Profile, error) {
			return New(pg.id, PostgreSQL, "", Version{Known: true, Major: 17}, pg.caps, Limits{1})
		}},
		{name: "builtin wrong capability", make: func() (Profile, error) {
			caps := pg.caps
			caps.TupleComparison = false
			return New(pg.id, PostgreSQL, "", Version{Known: true, Major: 17}, caps, Limits{pg.binds})
		}},
		{name: "unknown returning enum", make: func() (Profile, error) {
			caps := Capabilities{Returning: 99}
			return New("x", Custom, "custom", Version{}, caps, Limits{1})
		}},
		{name: "unknown upsert enum", make: func() (Profile, error) {
			caps := Capabilities{Upsert: 99}
			return New("x", Custom, "custom", Version{}, caps, Limits{1})
		}},
		{name: "unknown parent strategy", make: func() (Profile, error) {
			caps := Capabilities{PerParentLimit: 99}
			return New("x", Custom, "custom", Version{}, caps, Limits{1})
		}},
		{name: "window without window support", make: func() (Profile, error) {
			caps := Capabilities{PerParentLimit: PerParentLimitWindow}
			return New("x", Custom, "custom", Version{}, caps, Limits{1})
		}},
		{name: "upsert dependency", make: func() (Profile, error) {
			caps := Capabilities{ConflictTarget: true}
			return New("x", Custom, "custom", Version{}, caps, Limits{1})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.make()
			require.ErrorIs(t, err, ErrInvalidProfile)
		})
	}
}

func TestObserveRejectsInvalidQueryersAndEngines(t *testing.T) {
	var queryer Queryer
	for _, engine := range []EngineID{PostgreSQL, MySQL, SQLite, 0, Custom, EngineID(255)} {
		_, err := Observe(t.Context(), queryer, engine)
		require.ErrorIs(t, err, ErrVersionObservation)
	}
}
