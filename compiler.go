package rasql

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/nilcheck"
	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/stmt"
)

// Compiler renders statements for one engine profile and dialect without a
// database handle. Executing a mutation needs an Executor, which carries a
// connection and a discovered profile; seeing the SQL that mutation will send
// needs neither, so a caller that wants to log a write, review it, or assert
// on it in a test builds a Compiler from a profile it names itself.
//
// A Compiler applies the profile's capabilities the way execution does, so a
// statement an engine cannot express is refused here for the same reason and
// with the same error it would produce against that engine.
type Compiler struct {
	compiler *querycompile.Compiler
}

// Compiler returns a Compiler for p and d. It reports ErrInvalidEngineProfile
// when either is unusable, and ErrEngineProfileMismatch when the profile names
// a different engine than the dialect speaks.
func (p EngineProfile) Compiler(d dialect.Dialect) (Compiler, error) {
	c, err := p.queryCompiler(d)
	if err != nil {
		return Compiler{}, err
	}
	return Compiler{compiler: c}, nil
}

// EngineProfile returns the profile the Compiler renders for.
func (c Compiler) EngineProfile() EngineProfile {
	if c.compiler == nil {
		return EngineProfile{}
	}
	return EngineProfile{profile: c.compiler.EngineProfile()}
}

// Mutation renders plan into the SQL text and bound arguments ExecMutation
// would send for it. Bound values are copied on the way out, so the statement
// a caller inspects holds the same values a later render produces even if the
// caller writes through what it was given.
//
// It reports a PlanError with code engine_profile_unavailable when the
// Compiler was not built through EngineProfile.Compiler, and passes back the
// plan's own error for a native mutation, which carries rendered SQL rather
// than a write statement to compile.
func (c Compiler) Mutation(plan MutationPlan) (stmt.Statement, error) {
	if c.compiler == nil {
		return stmt.Statement{}, &PlanError{Code: "engine_profile_unavailable", Detail: "compiler has no engine profile"}
	}
	// See the matching comment in ExecMutation: a typed nil MutationPlan
	// passes == nil and would otherwise reach mutationPlan below.
	if nilcheck.Is(plan) {
		return stmt.Statement{}, fmt.Errorf("rasql: mutation plan must not be nil")
	}
	statement, err := plan.mutationPlan()
	if err != nil {
		return stmt.Statement{}, err
	}
	rendered, err := c.compiler.Write(statement)
	if err != nil {
		return stmt.Statement{}, err
	}
	compiled, err := unwrapBindTokens(rendered)
	if err != nil {
		return stmt.Statement{}, err
	}
	return compiled.Statement()
}

// CompileQuery renders q into the SQL text and bound arguments Rows and All
// would send for it, and is the read counterpart of Compiler.Mutation. Render
// answers the same question for a dialect alone; CompileQuery goes through an
// engine profile, so a query an engine cannot express is refused here the way
// execution refuses it.
//
// It is a function rather than a method because a method cannot take a type
// parameter of its own.
//
// It reports a PlanError with code engine_profile_unavailable when c was not
// built through EngineProfile.Compiler.
func CompileQuery[R any](c Compiler, q Query[R]) (stmt.Statement, error) {
	if c.compiler == nil {
		return stmt.Statement{}, &PlanError{Code: "engine_profile_unavailable", Detail: "compiler has no engine profile"}
	}
	compiled, err := compileQuery(c.compiler, q)
	if err != nil {
		return stmt.Statement{}, err
	}
	return compiled.Statement()
}
