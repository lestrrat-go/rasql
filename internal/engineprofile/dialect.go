package engineprofile

import "github.com/lestrrat-go/rasql/dialect"

func FromDialect(id string, d dialect.Dialect, v Version, caps Capabilities, limits Limits) (Profile, error) {
	if isNilDialect(d) {
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
