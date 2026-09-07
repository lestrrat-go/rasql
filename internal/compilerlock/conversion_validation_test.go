package compilerlock_test

import (
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/stretchr/testify/require"
)

func TestDirectConvertersPreserveNilAndEmptyArrays(t *testing.T) {
	for _, empty := range []bool{false, true} {
		var objects []compilerir.PhysicalObject
		if empty {
			objects = []compilerir.PhysicalObject{}
		}
		c := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite", Profile: "sqlite-3"}, Objects: objects}
		got := compilerlock.ToPhysical(compilerlock.File{Catalog: compilerlock.FromPhysical(c)})
		require.Equal(t, c.Objects == nil, got.Objects == nil)
	}
	q := compilerir.QueryAnalysis{ID: "q", Name: "Q", Parameters: []compilerir.SemanticValue{}, Results: []compilerir.SemanticValue{}, Engine: compilerir.EngineIdentity{Dialect: "sqlite", Profile: "sqlite-3"}}
	got := compilerlock.AnalysisFromQuery(compilerlock.QueryFromAnalysis(q))
	require.NotNil(t, got.Parameters)
	require.NotNil(t, got.Results)
}

func TestGenerationNamesAndDigestOperationsAreValidated(t *testing.T) {
	h := strings.Repeat("a", 64)
	base := compilerlock.DigestInputs{Source: compilerlock.SourceDigestInput{Record: compilerlock.SourceRecord{Kind: "live", Identity: "x"}, Engine: compilerlock.EngineRecord{Dialect: "sqlite", Profile: "sqlite-3"}}, Queries: []compilerlock.QueryDigestInput{{ID: "q", SQL: compilerlock.SourceFile{Path: "q.sql", SHA256: h}, Operation: "invalid", Cardinality: "many"}}, Generation: compilerir.GoConfig{Package: "p", Output: "out", Emitter: "compact"}}
	_, err := compilerlock.BuildDigests(base)
	require.Error(t, err)
	f := compilerlock.File{Format: 1, Compiler: "x", Source: compilerlock.SourceRecord{Kind: "live", Identity: "x"}, Engine: compilerlock.EngineRecord{Dialect: "sqlite", Profile: "sqlite-3"}, Catalog: compilerlock.CatalogRecord{Objects: []compilerlock.ObjectRecord{}}, Generation: compilerlock.GenerationRecord{Package: "p", Output: "out", Emitter: "compact", Objects: []compilerlock.ObjectNameRecord{}}, Digests: compilerlock.Digests{Source: h, Mappings: h, Queries: h, Generation: h}}
	f.Generation.Queries = []compilerlock.QueryNameRecord{{ID: "q", Function: "for", Result: "Result", Projection: "Projection", Decoder: "decode", File: "q.go"}}
	f.Queries = []compilerlock.QueryRecord{{ID: "q", Name: "Q", SQL: compilerlock.SourceFile{Path: "q.sql", SHA256: h}, Operation: "select", Cardinality: "many", Evidence: compilerlock.EngineEvidence{Dialect: "sqlite", Profile: "sqlite-3"}}}
	_, err = compilerlock.Encode(f)
	require.Error(t, err)
}
