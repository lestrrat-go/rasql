package rasql

import (
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

// PostgreSQL16 returns the built-in engine profile for PostgreSQL 16, at that
// spec's floor version, 16.0. Nothing in this package branches on the exact
// version a profile carries; it is read only to fill in error messages and to
// range-check a server Open discovered, so the floor version is a safe stand-in
// for "PostgreSQL 16, whatever the minor release."
func PostgreSQL16() EngineProfile {
	return builtinProfile("postgresql-16", 16, 0, 0)
}

// PostgreSQL17 returns the built-in engine profile for PostgreSQL 17, at that
// spec's floor version, 17.0. See PostgreSQL16 for why the floor version is a
// safe stand-in for the whole spec.
func PostgreSQL17() EngineProfile {
	return builtinProfile("postgresql-17", 17, 0, 0)
}

// MySQL84 returns the built-in engine profile for MySQL 8.4, at that spec's
// floor version, 8.4.0. See PostgreSQL16 for why the floor version is a safe
// stand-in for the whole spec.
func MySQL84() EngineProfile {
	return builtinProfile("mysql-8.4", 8, 4, 0)
}

// SQLite335 returns the built-in engine profile for SQLite 3.35, at that
// spec's floor version, 3.35.0. See PostgreSQL16 for why the floor version is
// a safe stand-in for the whole spec.
func SQLite335() EngineProfile {
	return builtinProfile("sqlite-3.35", 3, 35, 0)
}

// builtinProfile builds the built-in profile named id at major.minor.patch.
// Every call site passes one of the four built-in spec IDs at that spec's own
// floor version, which engineprofile.Builtin always accepts, so a failure
// here is a bug in this package rather than something a caller can act on.
func builtinProfile(id string, major, minor, patch uint16) EngineProfile {
	p, err := engineprofile.Builtin(id, engineprofile.Version{Known: true, Major: major, Minor: minor, Patch: patch})
	if err != nil {
		panic(fmt.Sprintf("rasql: built-in engine profile %q: %s", id, err))
	}
	return EngineProfile{profile: p}
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
