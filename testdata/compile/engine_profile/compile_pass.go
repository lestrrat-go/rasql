package engineprofilecompile

import rasql "github.com/lestrrat-go/rasql"

// CompilePass exercises the public profile surface from an external package.
func CompilePass() {
	_ = rasql.PostgreSQLEngine
	_ = rasql.MySQLEngine
	_ = rasql.SQLiteEngine
	_ = rasql.CustomEngine
	_ = rasql.EngineReturningNone
	_ = rasql.EngineReturningInsert
	_ = rasql.EngineReturningAllWrites
	_ = rasql.EngineUpsertNone
	_ = rasql.EngineUpsertOnConflict
	_ = rasql.EngineUpsertDuplicateKey
	_ = rasql.EnginePerParentLimitUnsupported
	_ = rasql.EnginePerParentLimitWindow
	_ = rasql.EnginePerParentLimitLateral

	p := rasql.PostgreSQL17()
	_ = p.ID()
	_ = p.Engine()
	_ = p.Version()
	_ = p.Capabilities()
	_ = p.Limits()
	_ = rasql.PostgreSQL16
	_ = rasql.MySQL84
	_ = rasql.SQLite335
	var open = rasql.Open
	_ = open
	_, _ = rasql.NewCustomEngineProfile("example", rasql.EngineVersion{Known: true, Major: 1}, rasql.EngineCapabilities{}, rasql.EngineLimits{MaxBindParameters: 1})
}
