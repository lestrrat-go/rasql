package rasql

import (
	"github.com/lestrrat-go/rasql/internal/graphfingerprint"
	"github.com/lestrrat-go/rasql/stmt"
)

// A stage's cache identity lives in internal/graphfingerprint, beside the bind
// and key packages it reads. These names stay so the rest of this package reads
// as before.
type graphStages = graphfingerprint.Stages
type graphStage = graphfingerprint.Stage

// engineProfileSnapshot is what a graph load reads about the engine behind an
// executor: the capability a per-parent limit needs, and the bind ceiling a
// batch is sized against.
type engineProfileSnapshot struct {
	Capabilities EngineCapabilities
	MaxBind      int
}

func graphPreencodeBaseOccurrences(base compiledQuery, encoded stmt.Statement, final compiledQuery) (compiledQuery, error) {
	return graphfingerprint.PreencodeBaseOccurrences(base, encoded, final)
}

func graphStageCacheable(compiled compiledQuery) bool {
	return graphfingerprint.StageCacheable(compiled)
}

func graphCloneValue(value any) any { return graphfingerprint.CloneValue(value) }

// executorCompilerProfile reads what a graph load needs from the engine profile
// an executor retained.
func executorCompilerProfile(executor Executor) engineProfileSnapshot {
	provider, ok := executor.(compilerProvider)
	if !ok || provider.queryCompiler() == nil {
		return engineProfileSnapshot{}
	}
	p := provider.queryCompiler().EngineProfile()
	return engineProfileSnapshot{Capabilities: p.Capabilities, MaxBind: p.Limits.MaxBindParameters}
}
