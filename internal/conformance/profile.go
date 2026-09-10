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

func (e Engine) Profile() (rasql.EngineProfile, error) {
	if err := e.Validate(); err != nil {
		return rasql.EngineProfile{}, err
	}
	return rasql.EngineProfileFromVersion(e.ProfileID, int(e.Version.Major), int(e.Version.Minor), int(e.Version.Patch))
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
