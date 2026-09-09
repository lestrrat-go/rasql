package compilerquery

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/schemasource"
	"github.com/stretchr/testify/require"
)

type declaredOnlySpy struct{ description Description }

func (s declaredOnlySpy) Describe(context.Context, DescribeRequest) (Description, error) {
	return s.description, nil
}

func TestAnalyzeDeclaredOnlyProducesDeclaredFacts(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "q.sql"), []byte(`SELECT id FROM users WHERE id = {{bind "x" users.id}} OR id = {{bind "x" users.id}}`), 0o600))
	nullable := false
	a, err := NewAnalyzer(Config{ModuleRoot: root, Queries: []QueryConfig{{ID: "q", Input: "q.sql", Engine: "sqlite", Function: "Q", Operation: "select", Cardinality: "many", Parameters: []ValueDeclaration{{Name: "x", Scalar: "unsigned_integer", Nullable: &nullable}}, Results: []ValueDeclaration{{Name: "id", Scalar: "decimal", Nullable: &nullable}}}}}, Describers{SQLite: declaredOnlySpy{description: Description{DeclaredOnly: true}}})
	require.NoError(t, err)
	profile, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 35})
	require.NoError(t, err)
	result, err := a.Analyze(t.Context(), schemasource.AnalysisRequest{Profile: profile})
	require.NoError(t, err)
	require.Equal(t, compilerir.CertaintyDeclared, result.Queries[0].Parameters[0].TypeCertainty)
	require.Equal(t, "integer", result.Queries[0].Parameters[0].LogicalKind)
	require.Equal(t, "decimal", result.Queries[0].Results[0].LogicalKind)
	require.Nil(t, result.Queries[0].Parameters[0].Native)
	require.Nil(t, result.Queries[0].Results[0].Integer)
}

func TestAnalyzeDeclaredOnlyRejectsObservationsAndUnknownScalar(t *testing.T) {
	nullable := false
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "q.sql"), []byte(`SELECT id FROM users WHERE id = {{bind "x" users.id}}`), 0o600))
	base := Config{ModuleRoot: root, Queries: []QueryConfig{{ID: "q", Input: "q.sql", Engine: "sqlite", Function: "Q", Operation: "select", Cardinality: "many", Parameters: []ValueDeclaration{{Name: "x", Scalar: "text", Nullable: &nullable}}, Results: []ValueDeclaration{{Name: "id", Scalar: "integer", Nullable: &nullable}}}}}
	profile, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 35})
	require.NoError(t, err)
	withObservation := declaredOnlySpy{description: Description{DeclaredOnly: true, Parameters: []ValueEvidence{{Name: "x"}}}}
	a, err := NewAnalyzer(base, Describers{SQLite: withObservation})
	require.NoError(t, err)
	_, err = a.Analyze(t.Context(), schemasource.AnalysisRequest{Profile: profile})
	require.ErrorContains(t, err, "declared-only description includes observations")
	base.Queries[0].Parameters[0].Scalar = "custom"
	a, err = NewAnalyzer(base, Describers{SQLite: declaredOnlySpy{description: Description{DeclaredOnly: true}}})
	require.NoError(t, err)
	_, err = a.Analyze(t.Context(), schemasource.AnalysisRequest{Profile: profile})
	require.ErrorContains(t, err, `scalar "custom" has no declared logical kind`)
}
