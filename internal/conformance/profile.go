package conformance

import (
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
)

type Engine struct {
	Name      string
	ProfileID string
	Version   rasql.EngineVersion
	Dialect   dialect.Dialect
}

var Engines = []Engine{
	{Name: "sqlite", ProfileID: "sqlite-3.35", Version: rasql.EngineVersion{Known: true, Major: 3, Minor: 35}, Dialect: dialect.SQLite()},
	{Name: "postgresql", ProfileID: "postgresql-16", Version: rasql.EngineVersion{Known: true, Major: 16}, Dialect: dialect.PostgreSQL()},
	{Name: "postgresql", ProfileID: "postgresql-17", Version: rasql.EngineVersion{Known: true, Major: 17}, Dialect: dialect.PostgreSQL()},
	{Name: "mysql", ProfileID: "mysql-8.4", Version: rasql.EngineVersion{Known: true, Major: 8, Minor: 4}, Dialect: dialect.MySQL()},
}

func (e Engine) Validate() error {
	if e.Name == "" || e.ProfileID == "" || e.Dialect == nil {
		return fmt.Errorf("conformance engine %q is incomplete", e.Name)
	}
	return nil
}

// Profile returns the built-in engine profile e names. It is the pinned
// spec-level profile, not one discovered against a live server: every field
// this package reads off it (Limits in particular) is a spec constant that
// does not vary with the exact version a live server reports.
func (e Engine) Profile() (rasql.EngineProfile, error) {
	if err := e.Validate(); err != nil {
		return rasql.EngineProfile{}, err
	}
	return builtinProfileByID(e.ProfileID)
}

// builtinProfileByID returns the built-in engine profile named id. See
// Profile's doc comment for why a pinned, undiscovered profile is the right
// value everywhere this package needs one.
func builtinProfileByID(id string) (rasql.EngineProfile, error) {
	switch id {
	case "postgresql-16":
		return rasql.PostgreSQL16(), nil
	case "postgresql-17":
		return rasql.PostgreSQL17(), nil
	case "mysql-8.4":
		return rasql.MySQL84(), nil
	case "sqlite-3.35":
		return rasql.SQLite335(), nil
	}
	return rasql.EngineProfile{}, fmt.Errorf("conformance: unknown profile %q", id)
}

func EngineByName(name string) (Engine, bool) {
	var found Engine
	for _, engine := range Engines {
		if engine.Name == name && (!found.Version.Known || engine.Version.Major > found.Version.Major || (engine.Version.Major == found.Version.Major && engine.Version.Minor > found.Version.Minor)) {
			found = engine
		}
	}
	return found, found.Name != ""
}
