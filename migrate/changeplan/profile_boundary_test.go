package changeplan_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type countingProfileSource struct {
	value                   engineprofile.Profile
	idCalls, engineCalls    int
	versionCalls, capsCalls int
	limitsCalls             int
}

func (s *countingProfileSource) ID() string {
	s.idCalls++
	return s.value.ID
}
func (s *countingProfileSource) Engine() changeplan.EngineID {
	s.engineCalls++
	return s.value.Engine
}
func (s *countingProfileSource) Version() changeplan.EngineVersion {
	s.versionCalls++
	return s.value.Version
}
func (s *countingProfileSource) Capabilities() changeplan.EngineCapabilities {
	s.capsCalls++
	return s.value.Capabilities
}
func (s *countingProfileSource) Limits() changeplan.EngineLimits {
	s.limitsCalls++
	return s.value.Limits
}

func (s *countingProfileSource) calls() []int {
	return []int{s.idCalls, s.engineCalls, s.versionCalls, s.capsCalls, s.limitsCalls}
}

type nilProfileSource struct{}

func (*nilProfileSource) ID() string                                  { panic("typed nil was called") }
func (*nilProfileSource) Engine() changeplan.EngineID                 { panic("typed nil was called") }
func (*nilProfileSource) Version() changeplan.EngineVersion           { panic("typed nil was called") }
func (*nilProfileSource) Capabilities() changeplan.EngineCapabilities { panic("typed nil was called") }
func (*nilProfileSource) Limits() changeplan.EngineLimits             { panic("typed nil was called") }

func TestProfileSnapshotsSourceExactlyOnce(t *testing.T) {
	value := testProfile(t)
	source := &countingProfileSource{value: engineprofile.Profile{
		ID:           value.ID(),
		Engine:       value.Engine(),
		Version:      value.Version(),
		Capabilities: value.Capabilities(),
		Limits:       value.Limits(),
	}}
	profile, err := changeplan.NewProfile(source)
	require.NoError(t, err)
	require.Equal(t, []int{1, 1, 1, 1, 1}, source.calls())

	source.value.ID = "custom:changed"
	source.value.Limits.MaxBindParameters = 1
	require.Equal(t, "sqlite-3.35", profile.ID())
	require.Equal(t, value.Limits(), profile.Limits())
}

func TestProfileRejectsTypedNilBeforeAccessors(t *testing.T) {
	var source *nilProfileSource
	_, err := changeplan.NewProfile(source)
	require.Error(t, err)
	_, err = changeplan.ProfileDigest(source)
	require.Error(t, err)
}

func TestProfileRejectsInvalidSourceMatrix(t *testing.T) {
	value := testProfile(t)
	cases := []engineprofile.Profile{
		{ID: "custom", Engine: engineprofile.Custom, Version: value.Version(), Capabilities: value.Capabilities(), Limits: value.Limits()},
		{ID: "custom:", Engine: engineprofile.Custom, Version: value.Version(), Capabilities: value.Capabilities(), Limits: value.Limits()},
		{ID: value.ID(), Engine: engineprofile.MySQL, Version: value.Version(), Capabilities: value.Capabilities(), Limits: value.Limits()},
	}
	for _, candidate := range cases {
		_, err := changeplan.NewProfile(testProfileSource{value: candidate})
		require.Error(t, err)
	}
}

func TestFromBaselineRejectsInvalidSources(t *testing.T) {
	history, err := changeplan.NewHistoryIdentity("main", "schema_migrations")
	require.NoError(t, err)
	value := testProfile(t)
	source := &countingProfileSource{value: engineprofile.Profile{
		ID: value.ID(), Engine: value.Engine(), Version: value.Version(),
		Capabilities: value.Capabilities(), Limits: value.Limits(),
	}}
	object, err := changeplan.NewCatalogObject("boundary-table",
		schema.TableDef{Schema: "main", Name: "tasks", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	baseline, err := changeplan.NewCatalog(value, "profile-boundary-source", []changeplan.CatalogObject{object})
	require.NoError(t, err)
	_, err = changeplan.FromBaseline(baseline, source, history, changeplan.ResolvedChanges{})
	require.Error(t, err)
	require.Equal(t, []int{1, 1, 1, 1, 1}, source.calls())

	var nilSource *nilProfileSource
	_, err = changeplan.FromBaseline(baseline, nilSource, history, changeplan.ResolvedChanges{})
	require.Error(t, err)
	_, err = changeplan.FromBaseline(changeplan.Catalog{}, testProfileSource{value: engineprofile.Profile{}}, history, changeplan.ResolvedChanges{})
	require.Error(t, err)
}
