package compilerir_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
)

func TestQueryAnalysisCloneCopiesTypedEvidence(t *testing.T) {
	q := compilerir.QueryAnalysis{Results: []compilerir.SemanticValue{{Name: "value", Scalar: "integer", LogicalKind: "integer", Integer: &compilerir.IntegerTypeFacts{Unsigned: true}}}}
	clone := q.Clone()
	clone.Results[0].Integer.Unsigned = false
	if !q.Results[0].Integer.Unsigned {
		t.Fatal("typed integer evidence was aliased")
	}
}
