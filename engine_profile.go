package rasql

import (
	"context"
	"fmt"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/querycompile"
)

type EngineID = engineprofile.EngineID
type EngineVersion = engineprofile.Version
type EngineCapabilities = engineprofile.Capabilities
type EngineLimits = engineprofile.Limits
type EngineReturningForms = engineprofile.ReturningForms
type EngineUpsertForm = engineprofile.UpsertForm
type EnginePerParentLimitStrategy = engineprofile.PerParentLimitStrategy
type EngineUpdateDefaultSupport = engineprofile.UpdateDefaultSupport

const (
	PostgreSQLEngine                = engineprofile.PostgreSQL
	MySQLEngine                     = engineprofile.MySQL
	SQLiteEngine                    = engineprofile.SQLite
	CustomEngine                    = engineprofile.Custom
	EngineReturningNone             = engineprofile.ReturningNone
	EngineReturningInsert           = engineprofile.ReturningInsert
	EngineReturningAllWrites        = engineprofile.ReturningInsertUpdateDelete
	EngineUpsertNone                = engineprofile.UpsertNone
	EngineUpsertOnConflict          = engineprofile.UpsertOnConflict
	EngineUpsertDuplicateKey        = engineprofile.UpsertDuplicateKey
	EnginePerParentLimitUnsupported = engineprofile.PerParentLimitUnsupported
	EnginePerParentLimitWindow      = engineprofile.PerParentLimitWindow
	EnginePerParentLimitLateral     = engineprofile.PerParentLimitLateral
	EngineUpdateDefaultUnsupported  = engineprofile.UpdateDefaultUnsupported
	EngineUpdateDefaultExpression   = engineprofile.UpdateDefaultExpression
)

type EngineProfile struct{ profile engineprofile.Profile }

var (
	ErrEngineVersionObservation = engineprofile.ErrVersionObservation
	ErrEngineVersionParse       = engineprofile.ErrVersionParse
	ErrUnknownEngineProfile     = engineprofile.ErrUnknownProfile
	ErrEngineProfileMismatch    = engineprofile.ErrProfileMismatch
	ErrInvalidEngineProfile     = engineprofile.ErrInvalidProfile
	ErrUnsupportedEngineFeature = engineprofile.ErrUnsupportedFeature
	ErrEngineBindLimit          = engineprofile.ErrBindLimit
)

func DiscoverEngineProfile(ctx context.Context, db DB, id string) (EngineProfile, error) {
	if profileEngine(id) == 0 {
		return EngineProfile{}, fmt.Errorf("%w: unknown profile %q", ErrUnknownEngineProfile, id)
	}
	if db.Dialect() == nil || db.Handle() == nil {
		return EngineProfile{}, fmt.Errorf("%w: database is empty", ErrInvalidEngineProfile)
	}
	engine := map[string]EngineID{"postgresql": PostgreSQLEngine, "mysql": MySQLEngine, "sqlite": SQLiteEngine}[db.Dialect().Name()]
	if engine == 0 {
		return EngineProfile{}, fmt.Errorf("%w: unknown dialect", ErrInvalidEngineProfile)
	}
	if want := profileEngine(id); want == 0 || want != engine {
		return EngineProfile{}, fmt.Errorf("%w: dialect and profile disagree", ErrEngineProfileMismatch)
	}
	p, err := engineprofile.Discover(ctx, db.Handle(), engine, id)
	return EngineProfile{profile: p}, err
}
func EngineProfileFromVersion(id string, major, minor, patch int) (EngineProfile, error) {
	if major < 0 || minor < 0 || patch < 0 || major > 65535 || minor > 65535 || patch > 65535 {
		return EngineProfile{}, fmt.Errorf("%w: version out of range", ErrInvalidEngineProfile)
	}
	p, err := engineprofile.Builtin(id, engineprofile.Version{Known: true, Major: uint16(major), Minor: uint16(minor), Patch: uint16(patch)})
	return EngineProfile{profile: p}, err
}
func NewCustomEngineProfile(name string, v EngineVersion, c EngineCapabilities, l EngineLimits) (EngineProfile, error) {
	name = strings.TrimSpace(name)
	p, err := engineprofile.New("custom:"+name, engineprofile.Custom, name, v, c, l)
	return EngineProfile{profile: p}, err
}
func (p EngineProfile) ID() string                       { return p.profile.ID }
func (p EngineProfile) Engine() EngineID                 { return p.profile.Engine }
func (p EngineProfile) Version() EngineVersion           { return p.profile.Version }
func (p EngineProfile) Capabilities() EngineCapabilities { return p.profile.Capabilities }
func (p EngineProfile) Limits() EngineLimits             { return p.profile.Limits }
func (p EngineProfile) queryCompiler(d dialect.Dialect) (*querycompile.Compiler, error) {
	if p.profile.ID == "" || d == nil {
		return nil, ErrInvalidEngineProfile
	}
	if p.profile.Engine != engineForDialect(d) {
		return nil, ErrEngineProfileMismatch
	}
	c, err := querycompile.NewWithDialect(p.profile, d)
	if err != nil {
		return nil, err
	}
	return &c, nil
}
func profileEngine(id string) EngineID {
	for _, x := range []string{"postgresql-16", "postgresql-17"} {
		if id == x {
			return PostgreSQLEngine
		}
	}
	if id == "mysql-8.4" {
		return MySQLEngine
	}
	if id == "sqlite-3.35" {
		return SQLiteEngine
	}
	return 0
}
func engineForDialect(d dialect.Dialect) EngineID {
	if d == nil {
		return 0
	}
	switch d.Name() {
	case "postgresql":
		return PostgreSQLEngine
	case "mysql":
		return MySQLEngine
	case "sqlite":
		return SQLiteEngine
	}
	return CustomEngine
}
