package engineprofile

import (
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
)

// capabilityRule is what a profile says about one dialect capability: the name
// ValidateDialect reports for it, and whether the profile allows it.
type capabilityRule struct {
	name    string
	allowed bool
}

// profileCapability reports what c says about capability. ok is false for a capability the
// profile does not decide, which constrainedDialect leaves to the dialect underneath.
//
// This is the only place a dialect capability maps onto the profile field behind it, so a
// new capability is one new case here.
func profileCapability(c Capabilities, capability dialect.Capability) (capabilityRule, bool) {
	switch capability {
	case dialect.CapabilityReturning:
		return capabilityRule{"returning", c.Returning != ReturningNone}, true
	case dialect.CapabilityUpsert:
		return capabilityRule{"upsert", c.Upsert != UpsertNone}, true
	case dialect.CapabilityConflictTarget:
		return capabilityRule{"conflict target", c.ConflictTarget}, true
	case dialect.CapabilityDefaultValues:
		return capabilityRule{"default values", c.DefaultValues}, true
	case dialect.CapabilityEmptyInsert:
		return capabilityRule{"empty insert", c.EmptyInsert}, true
	case dialect.CapabilityDefaultValuesUpsert:
		return capabilityRule{"default values upsert", c.DefaultValuesUpsert}, true
	case dialect.CapabilitySubqueryLimit:
		return capabilityRule{"subquery limit", c.SubqueryLimit}, true
	case dialect.CapabilityWriteSubqueryTarget:
		return capabilityRule{"write subquery target", c.WriteSubqueryTarget}, true
	case dialect.CapabilityQualifiedReference:
		return capabilityRule{"qualified reference", c.QualifiedReference}, true
	case dialect.CapabilityQualifiedIndexTarget:
		return capabilityRule{"qualified index target", c.QualifiedIndexTarget}, true
	case dialect.CapabilityQualifiedIndexName:
		return capabilityRule{"qualified index name", c.QualifiedIndexName}, true
	case dialect.CapabilityPartialIndex:
		return capabilityRule{"partial index", c.PartialIndex}, true
	case dialect.CapabilityMatchOperator:
		return capabilityRule{"match operator", c.MatchOperator}, true
	case dialect.CapabilityAggregateFilter:
		return capabilityRule{"aggregate filter", c.AggregateFilter}, true
	case dialect.CapabilitySelectForUpdate:
		return capabilityRule{"select for update", c.SelectForUpdate}, true
	case dialect.CapabilitySelectForShare:
		return capabilityRule{"select for share", c.SelectForShare}, true
	case dialect.CapabilitySelectLockOf:
		return capabilityRule{"select lock of", c.SelectLockOf}, true
	case dialect.CapabilitySelectLockNoWait:
		return capabilityRule{"select lock no wait", c.SelectLockNoWait}, true
	case dialect.CapabilitySelectLockSkipLocked:
		return capabilityRule{"select lock skip locked", c.SelectLockSkipLocked}, true
	case dialect.CapabilityUpsertConflictWhere:
		return capabilityRule{"upsert conflict where", c.UpsertConflictWhere}, true
	case dialect.CapabilityUpsertUpdateWhere:
		return capabilityRule{"upsert update where", c.UpsertUpdateWhere}, true
	case dialect.CapabilitySavepoint:
		return capabilityRule{"savepoint", c.Savepoints}, true
	}
	return capabilityRule{}, false
}

// upsertStyle is the dialect style that form asks for.
func upsertStyle(form UpsertForm) dialect.UpsertStyle {
	switch form {
	case UpsertOnConflict:
		return dialect.UpsertOnConflict
	case UpsertDuplicateKey:
		return dialect.UpsertDuplicateKey
	default:
		return dialect.UpsertUnsupported
	}
}

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
	// dialect.Capability is a bit flag, so walking the bits visits every capability
	// profileCapability knows, in the order the constants declare them.
	for capability := dialect.Capability(1); capability != 0; capability <<= 1 {
		rule, ok := profileCapability(p.Capabilities, capability)
		if !ok {
			continue
		}
		if d.Supports(capability) != rule.allowed {
			return &ProfileError{Code: ErrInvalidProfile, Engine: p.Engine, Version: p.Version, Feature: rule.name, Detail: "dialect and capabilities disagree"}
		}
	}
	if d.UpsertStyle() != upsertStyle(p.Capabilities.Upsert) {
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
	return upsertStyle(d.profile.Capabilities.Upsert)
}
func (d constrainedDialect) Supports(capability dialect.Capability) bool {
	rule, ok := profileCapability(d.profile.Capabilities, capability)
	if !ok {
		return d.base.Supports(capability)
	}
	return rule.allowed
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
