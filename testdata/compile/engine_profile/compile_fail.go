//go:build compile_fail

package engineprofilecompile

import rasql "github.com/lestrrat-go/rasql"

// This fixture is compiled separately by the compile-failure gate.
func CompileFail(p rasql.EngineProfile) string { return p.profile.ID }
