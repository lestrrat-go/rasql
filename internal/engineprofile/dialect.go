package engineprofile

import "github.com/lestrrat-go/rasql/dialect"

func FromDialect(id string, d dialect.Dialect, v Version, caps Capabilities, limits Limits) (Profile, error) {
	if d == nil {
		return Profile{}, &ProfileError{Code: ErrInvalidProfile, Detail: "dialect must not be nil"}
	}
	engine := map[string]EngineID{"postgresql": PostgreSQL, "mysql": MySQL, "sqlite": SQLite}[d.Name()]
	if engine == 0 {
		if id == "" {
			return Profile{}, &ProfileError{Code: ErrInvalidProfile, Detail: "unknown dialect"}
		}
		engine = Custom
	}
	if engine != Custom {
		checks := []struct {
			name string
			want bool
			got  bool
		}{{"returning", d.Supports(dialect.CapabilityReturning), caps.Returning != ReturningNone}, {"upsert", d.Supports(dialect.CapabilityUpsert), caps.Upsert != UpsertNone}, {"conflict target", d.Supports(dialect.CapabilityConflictTarget), caps.ConflictTarget}, {"default values", d.Supports(dialect.CapabilityDefaultValues), caps.DefaultValues}, {"empty insert", d.Supports(dialect.CapabilityEmptyInsert), caps.EmptyInsert}, {"default values upsert", d.Supports(dialect.CapabilityDefaultValuesUpsert), caps.DefaultValuesUpsert}, {"subquery limit", d.Supports(dialect.CapabilitySubqueryLimit), caps.SubqueryLimit}, {"write subquery target", d.Supports(dialect.CapabilityWriteSubqueryTarget), caps.WriteSubqueryTarget}, {"qualified reference", d.Supports(dialect.CapabilityQualifiedReference), caps.QualifiedReference}, {"qualified index target", d.Supports(dialect.CapabilityQualifiedIndexTarget), caps.QualifiedIndexTarget}, {"qualified index name", d.Supports(dialect.CapabilityQualifiedIndexName), caps.QualifiedIndexName}, {"partial index", d.Supports(dialect.CapabilityPartialIndex), caps.PartialIndex}, {"match", d.Supports(dialect.CapabilityMatchOperator), caps.MatchOperator}, {"aggregate filter", d.Supports(dialect.CapabilityAggregateFilter), caps.AggregateFilter}, {"savepoint", d.Supports(dialect.CapabilitySavepoint), caps.Savepoints}}
		for _, c := range checks {
			if c.want != c.got {
				return Profile{}, &ProfileError{Code: ErrInvalidProfile, Engine: engine, Feature: c.name, Detail: "dialect and capabilities disagree"}
			}
		}
	}
	return New(id, engine, "", v, caps, limits)
}
