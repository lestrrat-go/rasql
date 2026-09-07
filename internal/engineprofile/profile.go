package engineprofile

import (
	"errors"
	"fmt"
)

type EngineID uint8

const (
	PostgreSQL EngineID = iota + 1
	MySQL
	SQLite
	Custom
)

type Version struct {
	Known               bool
	Major, Minor, Patch uint16
}
type ReturningForms uint8

const (
	ReturningNone ReturningForms = iota
	ReturningInsert
	ReturningInsertUpdateDelete
)

type UpsertForm uint8

const (
	UpsertNone UpsertForm = iota
	UpsertOnConflict
	UpsertDuplicateKey
)

type PerParentLimitStrategy uint8

const (
	PerParentLimitUnsupported PerParentLimitStrategy = iota
	PerParentLimitWindow
	PerParentLimitLateral
)

type Capabilities struct {
	Returning                                                                             ReturningForms
	Upsert                                                                                UpsertForm
	ConflictTarget, DefaultValues, EmptyInsert, DefaultValuesUpsert                       bool
	SubqueryLimit, WriteSubqueryTarget, PartialIndex, AggregateFilter                     bool
	QualifiedReference, QualifiedIndexTarget, QualifiedIndexName, MatchOperator           bool
	SelectForUpdate, SelectForShare, SelectLockOf, SelectLockNoWait, SelectLockSkipLocked bool
	UpsertConflictWhere, UpsertUpdateWhere                                                bool
	WindowFunctions, LateralJoins, Savepoints, TransactionalDDL                           bool
	ExplicitNullOrdering, TupleComparison                                                 bool
	PerParentLimit                                                                        PerParentLimitStrategy
}
type Limits struct{ MaxBindParameters int }
type Profile struct {
	ID           string
	Engine       EngineID
	CustomName   string
	Version      Version
	Capabilities Capabilities
	Limits       Limits
}

var (
	ErrInvalidProfile     = errors.New("engine profile: invalid profile")
	ErrUnsupportedFeature = errors.New("engine profile: unsupported feature")
	ErrUnsupportedVersion = errors.New("engine profile: unsupported version")
	ErrBindLimit          = errors.New("engine profile: bind limit exceeded")
)

type ProfileError struct {
	Code                       error
	Engine                     EngineID
	Version                    Version
	Operation, Feature, Detail string
}

func (e *ProfileError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("engine profile: %s: %s", e.Code, e.Detail)
	}
	return e.Code.Error()
}
func (e *ProfileError) Unwrap() error { return e.Code }

func validEngine(e EngineID) bool { return e >= PostgreSQL && e <= Custom }
func less(a, b Version) bool {
	return a.Major < b.Major || a.Major == b.Major && (a.Minor < b.Minor || a.Minor == b.Minor && a.Patch < b.Patch)
}

func New(profileID string, engine EngineID, customName string, version Version, caps Capabilities, limits Limits) (Profile, error) {
	if !validEngine(engine) || profileID == "" || limits.MaxBindParameters <= 0 {
		return Profile{}, &ProfileError{Code: ErrInvalidProfile, Engine: engine, Version: version, Detail: "invalid identity or bind limit"}
	}
	if version.Known && version.Major == 0 {
		return Profile{}, &ProfileError{Code: ErrInvalidProfile, Engine: engine, Version: version, Detail: "known version has zero major"}
	}
	if !version.Known && engine != Custom {
		return Profile{}, &ProfileError{Code: ErrInvalidProfile, Engine: engine, Version: version, Detail: "built-in profile requires a known version"}
	}
	if engine == Custom && customName == "" {
		return Profile{}, &ProfileError{Code: ErrInvalidProfile, Engine: engine, Version: version, Detail: "custom name is required"}
	}
	if engine != Custom {
		var known *profileSpec
		for i := range specs() {
			if specs()[i].id == profileID {
				x := specs()[i]
				known = &x
				break
			}
		}
		if known == nil || known.engine != engine || limits.MaxBindParameters != known.binds || caps != known.caps {
			return Profile{}, &ProfileError{Code: ErrInvalidProfile, Engine: engine, Version: version, Detail: "built-in capabilities or limits do not match profile"}
		}
	}
	if caps.PerParentLimit == PerParentLimitWindow && !caps.WindowFunctions || caps.PerParentLimit == PerParentLimitLateral && !caps.LateralJoins {
		return Profile{}, &ProfileError{Code: ErrInvalidProfile, Engine: engine, Version: version, Detail: "per-parent strategy lacks matching capability"}
	}
	return Profile{ID: profileID, Engine: engine, CustomName: customName, Version: version, Capabilities: caps, Limits: limits}, nil
}

type profileSpec struct {
	id                 string
	engine             EngineID
	major, minMinor    uint16
	maxMinor, maxPatch uint16
	caps               Capabilities
	binds              int
}

func specs() []profileSpec {
	pg := Capabilities{Returning: ReturningInsertUpdateDelete, Upsert: UpsertOnConflict, ConflictTarget: true, DefaultValues: true, DefaultValuesUpsert: true, SubqueryLimit: true, WriteSubqueryTarget: true, QualifiedReference: true, QualifiedIndexTarget: true, PartialIndex: true, AggregateFilter: true, SelectForUpdate: true, SelectForShare: true, SelectLockOf: true, SelectLockNoWait: true, SelectLockSkipLocked: true, UpsertConflictWhere: true, UpsertUpdateWhere: true, WindowFunctions: true, LateralJoins: true, Savepoints: true, TransactionalDDL: true, ExplicitNullOrdering: true, TupleComparison: true, PerParentLimit: PerParentLimitWindow}
	m := Capabilities{Upsert: UpsertDuplicateKey, EmptyInsert: true, DefaultValuesUpsert: true, QualifiedReference: true, QualifiedIndexTarget: true, SelectForUpdate: true, SelectForShare: true, SelectLockOf: true, SelectLockNoWait: true, SelectLockSkipLocked: true, WindowFunctions: true, LateralJoins: true, Savepoints: true, TupleComparison: true, PerParentLimit: PerParentLimitWindow}
	s := Capabilities{Returning: ReturningInsertUpdateDelete, Upsert: UpsertOnConflict, ConflictTarget: true, DefaultValues: true, SubqueryLimit: true, WriteSubqueryTarget: true, QualifiedIndexName: true, MatchOperator: true, PartialIndex: true, AggregateFilter: true, UpsertConflictWhere: true, UpsertUpdateWhere: true, WindowFunctions: true, Savepoints: true, TransactionalDDL: true, ExplicitNullOrdering: true, TupleComparison: true, PerParentLimit: PerParentLimitWindow}
	return []profileSpec{{"postgresql-16", PostgreSQL, 16, 0, 65535, 0, pg, 65535}, {"postgresql-17", PostgreSQL, 17, 0, 65535, 0, pg, 65535}, {"mysql-8.4", MySQL, 8, 4, 4, 65535, m, 65535}, {"sqlite-3.35", SQLite, 3, 35, 65535, 65535, s, 999}}
}
func Resolve(profileID string, observed ObservedIdentity) (Profile, error) {
	for _, s := range specs() {
		if s.id == profileID {
			if observed.Engine != s.engine {
				return Profile{}, &ProfileError{Code: ErrInvalidProfile, Engine: observed.Engine, Version: observed.Version, Detail: "observed engine does not match profile"}
			}
			if !observed.Version.Known || observed.Version.Major != s.major || observed.Version.Minor < s.minMinor || observed.Version.Minor > s.maxMinor || observed.Version.Patch > s.maxPatch {
				return Profile{}, &ProfileError{Code: ErrUnsupportedVersion, Engine: s.engine, Version: observed.Version, Detail: "observed version is outside supported range"}
			}
			return New(s.id, s.engine, "", observed.Version, s.caps, Limits{s.binds})
		}
	}
	return Profile{}, &ProfileError{Code: ErrInvalidProfile, Detail: "unknown profile " + profileID}
}
func Builtin(profileID string, v Version) (Profile, error) {
	return Resolve(profileID, ObservedIdentity{Engine: engineForProfile(profileID), Version: v})
}
func engineForProfile(id string) EngineID {
	for _, s := range specs() {
		if s.id == id {
			return s.engine
		}
	}
	return 0
}
