package compilerlock_test

import (
	"encoding/json"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
)

func TestQueryEvidenceRoundTripAndClone(t *testing.T) {
	q := compilerir.QueryAnalysis{ID: "q", Parameters: []compilerir.SemanticValue{{Name: "id", Scalar: "domain.ID", LogicalKind: "uuid", TypeCertainty: compilerir.CertaintyKnown, NullabilityCertainty: compilerir.CertaintyDeclared, Native: &compilerir.NativeType{Dialect: "postgresql", Name: "uuid", Kind: "builtin", Arguments: []string{"a"}, Element: &compilerir.NativeType{Dialect: "postgresql", Name: "uuid", Kind: "builtin"}}}}}
	record := compilerlock.QueryFromAnalysis(q)
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var decoded compilerlock.QueryRecord
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	got := compilerlock.AnalysisFromQuery(decoded)
	if got.Parameters[0].Native.Element.Name != "uuid" || got.Parameters[0].Native.Arguments[0] != "a" {
		t.Fatalf("got=%#v", got)
	}
	clone := q.Clone()
	clone.Parameters[0].Native.Arguments[0] = "changed"
	clone.Parameters[0].Native.Element.Name = "changed"
	if q.Parameters[0].Native.Arguments[0] != "a" || q.Parameters[0].Native.Element.Name != "uuid" {
		t.Fatal("QueryAnalysis.Clone aliases evidence")
	}
}

func TestQueryEvidenceRejectsInvalidTypedFacts(t *testing.T) {
	f := compilerlock.File{Format: compilerlock.FormatVersion, Compiler: "rasql", Queries: []compilerlock.QueryRecord{{ID: "q", Parameters: []compilerlock.ValueRecord{{Name: "x", Scalar: "text", LogicalKind: "text", Integer: &compilerlock.IntegerTypeFactsRecord{}}}}}}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compilerlock.Decode(data); err == nil {
		t.Fatal("expected integer fact validation error")
	}
}
