package engineprofile

import "github.com/lestrrat-go/rasql/dialect"

// FromDialect builds a Profile for d. The engine comes from d.Name(): "postgresql", "mysql" and "sqlite"
// select the matching built-in engine, and any other name makes the profile custom and requires a non-empty
// id. It returns a *ProfileError with Code ErrInvalidProfile when the name is unrecognized and id is empty,
// and when ValidateDialect finds d and caps disagreeing.
//
// `d` must not be nil.
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
	if engine == Custom {
		if id == "" {
			return Profile{}, &ProfileError{Code: ErrInvalidProfile, Detail: "custom profile ID is required"}
		}
	}
	customName := ""
	if engine == Custom {
		customName = d.Name()
	}
	p, err := New(id, engine, customName, v, caps, limits)
	if err != nil {
		return Profile{}, err
	}
	if err := ValidateDialect(d, p); err != nil {
		return Profile{}, err
	}
	return p, nil
}
