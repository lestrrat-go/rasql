package engineprofile

import (
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
)

// ValidateDialect checks the syntax capabilities that the dialect interface
// exposes. Runtime capabilities such as window functions remain profile data.
//
// `d` must not be nil.
func ValidateDialect(d dialect.Dialect, p Profile) error {
	if d == nil {
		return &ProfileError{Code: ErrInvalidProfile, Engine: p.Engine, Version: p.Version, Detail: "dialect must not be nil"}
	}
	if p.Engine != Custom && d.Name() != engineName(p.Engine) {
		return &ProfileError{Code: ErrInvalidProfile, Engine: p.Engine, Version: p.Version, Detail: "dialect and profile disagree"}
	}
	if p.Engine == Custom && d.Name() != p.CustomName {
		return &ProfileError{Code: ErrInvalidProfile, Engine: p.Engine, Version: p.Version, Detail: "dialect and custom profile disagree"}
	}
	checks := []struct {
		name string
		cap  dialect.Capability
		want bool
	}{
		{"returning", dialect.CapabilityReturning, p.Capabilities.Returning != ReturningNone},
		{"upsert", dialect.CapabilityUpsert, p.Capabilities.Upsert != UpsertNone},
		{"conflict target", dialect.CapabilityConflictTarget, p.Capabilities.ConflictTarget},
		{"default values", dialect.CapabilityDefaultValues, p.Capabilities.DefaultValues},
		{"empty insert", dialect.CapabilityEmptyInsert, p.Capabilities.EmptyInsert},
		{"default values upsert", dialect.CapabilityDefaultValuesUpsert, p.Capabilities.DefaultValuesUpsert},
		{"subquery limit", dialect.CapabilitySubqueryLimit, p.Capabilities.SubqueryLimit},
		{"write subquery target", dialect.CapabilityWriteSubqueryTarget, p.Capabilities.WriteSubqueryTarget},
		{"qualified reference", dialect.CapabilityQualifiedReference, p.Capabilities.QualifiedReference},
		{"qualified index target", dialect.CapabilityQualifiedIndexTarget, p.Capabilities.QualifiedIndexTarget},
		{"qualified index name", dialect.CapabilityQualifiedIndexName, p.Capabilities.QualifiedIndexName},
		{"partial index", dialect.CapabilityPartialIndex, p.Capabilities.PartialIndex},
		{"match operator", dialect.CapabilityMatchOperator, p.Capabilities.MatchOperator},
		{"aggregate filter", dialect.CapabilityAggregateFilter, p.Capabilities.AggregateFilter},
		{"select for update", dialect.CapabilitySelectForUpdate, p.Capabilities.SelectForUpdate},
		{"select for share", dialect.CapabilitySelectForShare, p.Capabilities.SelectForShare},
		{"select lock of", dialect.CapabilitySelectLockOf, p.Capabilities.SelectLockOf},
		{"select lock no wait", dialect.CapabilitySelectLockNoWait, p.Capabilities.SelectLockNoWait},
		{"select lock skip locked", dialect.CapabilitySelectLockSkipLocked, p.Capabilities.SelectLockSkipLocked},
		{"upsert conflict where", dialect.CapabilityUpsertConflictWhere, p.Capabilities.UpsertConflictWhere},
		{"upsert update where", dialect.CapabilityUpsertUpdateWhere, p.Capabilities.UpsertUpdateWhere},
		{"savepoint", dialect.CapabilitySavepoint, p.Capabilities.Savepoints},
	}
	for _, check := range checks {
		if d.Supports(check.cap) != check.want {
			return &ProfileError{Code: ErrInvalidProfile, Engine: p.Engine, Version: p.Version, Feature: check.name, Detail: "dialect and capabilities disagree"}
		}
	}
	wantStyle := dialect.UpsertUnsupported
	switch p.Capabilities.Upsert {
	case UpsertOnConflict:
		wantStyle = dialect.UpsertOnConflict
	case UpsertDuplicateKey:
		wantStyle = dialect.UpsertDuplicateKey
	}
	if d.UpsertStyle() != wantStyle {
		return &ProfileError{Code: ErrInvalidProfile, Engine: p.Engine, Version: p.Version, Feature: "upsert style", Detail: "dialect and capabilities disagree"}
	}
	return nil
}

func engineName(engine EngineID) string {
	switch engine {
	case PostgreSQL:
		return "postgresql"
	case MySQL:
		return "mysql"
	case SQLite:
		return "sqlite"
	default:
		return ""
	}
}

type constrainedDialect struct {
	base    dialect.Dialect
	profile Profile
}

func (d constrainedDialect) Name() string { return d.base.Name() }
func (d constrainedDialect) QuoteIdentifier(s string) (string, error) {
	return d.base.QuoteIdentifier(s)
}
func (d constrainedDialect) Placeholder(n int) (string, error)           { return d.base.Placeholder(n) }
func (d constrainedDialect) TypeName(c schema.ColumnDef) (string, error) { return d.base.TypeName(c) }
func (d constrainedDialect) UpsertStyle() dialect.UpsertStyle {
	switch d.profile.Capabilities.Upsert {
	case UpsertOnConflict:
		return dialect.UpsertOnConflict
	case UpsertDuplicateKey:
		return dialect.UpsertDuplicateKey
	default:
		return dialect.UpsertUnsupported
	}
}
func (d constrainedDialect) Supports(capability dialect.Capability) bool {
	c := d.profile.Capabilities
	switch capability {
	case dialect.CapabilityReturning:
		return c.Returning != ReturningNone
	case dialect.CapabilityUpsert:
		return c.Upsert != UpsertNone
	case dialect.CapabilityConflictTarget:
		return c.ConflictTarget
	case dialect.CapabilityDefaultValues:
		return c.DefaultValues
	case dialect.CapabilityEmptyInsert:
		return c.EmptyInsert
	case dialect.CapabilityDefaultValuesUpsert:
		return c.DefaultValuesUpsert
	case dialect.CapabilitySubqueryLimit:
		return c.SubqueryLimit
	case dialect.CapabilityWriteSubqueryTarget:
		return c.WriteSubqueryTarget
	case dialect.CapabilityQualifiedReference:
		return c.QualifiedReference
	case dialect.CapabilityQualifiedIndexTarget:
		return c.QualifiedIndexTarget
	case dialect.CapabilityQualifiedIndexName:
		return c.QualifiedIndexName
	case dialect.CapabilityPartialIndex:
		return c.PartialIndex
	case dialect.CapabilityMatchOperator:
		return c.MatchOperator
	case dialect.CapabilityAggregateFilter:
		return c.AggregateFilter
	case dialect.CapabilitySelectForUpdate:
		return c.SelectForUpdate
	case dialect.CapabilitySelectForShare:
		return c.SelectForShare
	case dialect.CapabilitySelectLockOf:
		return c.SelectLockOf
	case dialect.CapabilitySelectLockNoWait:
		return c.SelectLockNoWait
	case dialect.CapabilitySelectLockSkipLocked:
		return c.SelectLockSkipLocked
	case dialect.CapabilityUpsertConflictWhere:
		return c.UpsertConflictWhere
	case dialect.CapabilityUpsertUpdateWhere:
		return c.UpsertUpdateWhere
	case dialect.CapabilitySavepoint:
		return c.Savepoints
	default:
		return d.base.Supports(capability)
	}
}
func (d constrainedDialect) Compiler() dialect.Compiler {
	if provider, ok := d.base.(dialect.CompilerProvider); ok {
		return provider.Compiler()
	}
	return nil
}
func (d constrainedDialect) NativeTypeName(n schema.NativeTypeDef) (string, bool, error) {
	if namer, ok := d.base.(dialect.NativeTypeNamer); ok {
		return namer.NativeTypeName(n)
	}
	return "", false, nil
}
func (d constrainedDialect) IdentifiersEqual(left, right string) bool {
	return dialect.IdentifiersEqual(d.base, left, right)
}

func constrainDialect(p Profile, d dialect.Dialect) dialect.Dialect {
	return constrainedDialect{base: d, profile: p}
}

func ConstrainDialect(p Profile, d dialect.Dialect) dialect.Dialect { return constrainDialect(p, d) }
