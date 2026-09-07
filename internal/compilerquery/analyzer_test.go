package compilerquery

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/schemasource"
)

func TestAnalyzeValidatesBeforeDescribeAndSnapshotsQuery(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "find.sql")
	if err := os.WriteFile(path, []byte(`SELECT id FROM users WHERE id = {{bind "id" users.id}}`), 0600); err != nil {
		t.Fatal(err)
	}
	nullable := false
	spy := &analysisSpy{}
	a, err := NewAnalyzer(Config{ModuleRoot: root, Queries: []QueryConfig{{ID: "find", Input: "find.sql", Engine: "sqlite", Function: "Find", Operation: "select", Cardinality: "many", Parameters: []ValueDeclaration{{Name: "id", Scalar: "integer", Nullable: &nullable}}, Results: []ValueDeclaration{{Name: "id", Scalar: "integer", Nullable: &nullable}}}}}, Describers{SQLite: spy})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 35})
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.Analyze(t.Context(), schemasource.AnalysisRequest{Profile: profile})
	if err != nil {
		t.Fatal(err)
	}
	if spy.calls != 1 || len(result.Queries) != 1 || len(result.Snapshots) != 1 {
		t.Fatalf("calls=%d result=%#v", spy.calls, result)
	}
	if result.Queries[0].SQLPath != "find.sql" || result.Snapshots[0].Path() != "find.sql" {
		t.Fatalf("query=%#v snapshots=%#v", result.Queries[0], result.Snapshots)
	}
}

type analysisSpy struct{ calls int }

func (s *analysisSpy) Describe(_ context.Context, request DescribeRequest) (Description, error) {
	s.calls++
	return Description{Parameters: []ValueEvidence{{Name: "id", Type: TypeEvidence{LogicalKind: "integer", Certainty: compilerir.CertaintyKnown}}}, Results: []ValueEvidence{{Name: "id", Type: TypeEvidence{LogicalKind: "integer", Certainty: compilerir.CertaintyKnown}}}}, nil
}
