package rasql

import (
	"github.com/lestrrat-go/rasql/internal/graphfingerprint"
	"github.com/lestrrat-go/rasql/stmt"
)

// The cache key lives in internal/graphfingerprint, beside the bind and key
// packages it reads. These names stay so the rest of this package reads as
// before.
type engineProfileSnapshot = graphfingerprint.Profile
type graphCacheFingerprint = graphfingerprint.Fingerprint
type graphFingerprintStage = graphfingerprint.Stage

func graphPreencodeBaseOccurrences(base compiledQuery, encoded stmt.Statement, final compiledQuery) (compiledQuery, error) {
	return graphfingerprint.PreencodeBaseOccurrences(base, encoded, final)
}

func graphStageCacheable(compiled compiledQuery) bool {
	return graphfingerprint.StageCacheable(compiled)
}

func graphCloneValue(value any) any { return graphfingerprint.CloneValue(value) }

func graphInvocationFingerprint(stage graphFingerprintStage, profile engineProfileSnapshot) (graphCacheFingerprint, error) {
	return graphfingerprint.Invocation(stage, profile)
}

// executorCompilerProfile reads the engine profile an executor retained, which
// is what the cache key records about the engine.
func executorCompilerProfile(executor Executor) engineProfileSnapshot {
	provider, ok := executor.(compilerProvider)
	if !ok || provider.queryCompiler() == nil {
		return engineProfileSnapshot{}
	}
	p := provider.queryCompiler().EngineProfile()
	dialectName := ""
	if dialect := executor.Dialect(); dialect != nil {
		dialectName = dialect.Name()
	}
	return engineProfileSnapshot{
		Dialect: dialectName, ID: p.ID, Engine: p.Engine, CustomName: p.CustomName,
		Version: p.Version, Limits: p.Limits, Capabilities: p.Capabilities,
		MaxBind: p.Limits.MaxBindParameters,
	}
}
