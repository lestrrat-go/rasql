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

func TestAnalyzePassesAllRepeatedPostgreSQLOccurrencesToDescriber(t *testing.T) {
	root := t.TempDir()
	sql := `SELECT id FROM users WHERE id = {{bind "x" users.id}} OR id = {{bind "x" users.id}} OR id = {{bind "y" users.id}}`
	require.NoError(t, os.WriteFile(filepath.Join(root, "q.sql"), []byte(sql), 0o600))
	nullable := false
	spy := &occurrenceSpy{}
	a, err := NewAnalyzer(Config{ModuleRoot: root, Queries: []QueryConfig{{ID: "q", Input: "q.sql", Engine: "postgresql", Function: "Q", Operation: "select", Cardinality: "many", Parameters: []ValueDeclaration{{Name: "x", Nullable: &nullable}, {Name: "y", Nullable: &nullable}}, Results: []ValueDeclaration{{Name: "id", Nullable: &nullable}, {Name: "kind", Nullable: &nullable}}}}}, Describers{PostgreSQL: spy})
	require.NoError(t, err)
	profile, err := engineprofile.Builtin("postgresql-16", engineprofile.Version{Known: true, Major: 16})
	require.NoError(t, err)
	result, err := a.Analyze(t.Context(), schemasource.AnalysisRequest{Profile: profile})
	require.NoError(t, err)
	require.Equal(t, []string{"x", "x", "y"}, spy.names)
	require.Equal(t, []string{"x", "y"}, []string{result.Queries[0].Parameters[0].Name, result.Queries[0].Parameters[1].Name})
	require.Equal(t, []string{"text", "integer"}, []string{result.Queries[0].Parameters[0].LogicalKind, result.Queries[0].Parameters[1].LogicalKind})
	require.Equal(t, []string{"id", "kind"}, []string{result.Queries[0].Results[0].Name, result.Queries[0].Results[1].Name})
	require.Equal(t, []string{"integer", "text"}, []string{result.Queries[0].Results[0].LogicalKind, result.Queries[0].Results[1].LogicalKind})
}

type occurrenceSpy struct{ names []string }

func (s *occurrenceSpy) Describe(_ context.Context, request DescribeRequest) (Description, error) {
	s.names = append([]string(nil), request.ParameterNames...)
	return Description{Parameters: []ValueEvidence{{Name: "x", Type: TypeEvidence{LogicalKind: "text", Certainty: compilerir.CertaintyKnown}}, {Name: "x", Type: TypeEvidence{LogicalKind: "text", Certainty: compilerir.CertaintyKnown}}, {Name: "y", Type: TypeEvidence{LogicalKind: "integer", Certainty: compilerir.CertaintyKnown}}}, Results: []ValueEvidence{{Name: "id", Type: TypeEvidence{LogicalKind: "integer", Certainty: compilerir.CertaintyKnown}}, {Name: "kind", Type: TypeEvidence{LogicalKind: "text", Certainty: compilerir.CertaintyKnown}}}}, nil
}

func TestMergeValuesAcceptsThreeOccurrencesAndKeepsResultsIndependent(t *testing.T) {
	nullable := false
	native := &compilerir.NativeType{Dialect: "postgresql", Name: "text", Kind: "builtin", Arguments: []string{"1"}}
	declarations := []ValueDeclaration{{Name: "x", Scalar: "text", Nullable: &nullable}, {Name: "y", Scalar: "integer", Nullable: &nullable}}
	observed := []ValueEvidence{{Name: "x", Type: TypeEvidence{LogicalKind: "text", Native: native, Certainty: compilerir.CertaintyKnown}}, {Name: "x", Type: TypeEvidence{LogicalKind: "text", Native: cloneNative(native), Certainty: compilerir.CertaintyKnown}}, {Name: "y", Type: TypeEvidence{LogicalKind: "integer", Certainty: compilerir.CertaintyKnown}}}
	got, err := mergeValues(declarations, observed, []string{"x", "x", "y"}, compilerir.MappingConfig{})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "text", got[0].LogicalKind)
	require.Equal(t, "integer", got[1].LogicalKind)
	got[0].LogicalKind = "changed"
	require.Equal(t, "integer", got[1].LogicalKind)
}

func TestMergeValuesRejectsMissingOccurrenceBeforeCollapse(t *testing.T) {
	_, err := mergeValues([]ValueDeclaration{{Name: "x", Scalar: "text"}}, []ValueEvidence{{Name: "x"}}, []string{"x", "x", "y"}, compilerir.MappingConfig{})
	require.ErrorContains(t, err, "observed occurrence count")
}

func TestMergeValuesRejectsCompleteRepeatedFactConflicts(t *testing.T) {
	cases := []struct {
		name string
		edit func(*ValueEvidence)
	}{
		{"native nested element", func(v *ValueEvidence) {
			v.Type.Native = &compilerir.NativeType{Dialect: "postgresql", Name: "array", Kind: "array", Element: &compilerir.NativeType{Dialect: "postgresql", Name: "text", Kind: "builtin"}}
		}},
		{"native argument", func(v *ValueEvidence) {
			v.Type.Native = &compilerir.NativeType{Dialect: "postgresql", Name: "text", Kind: "builtin", Arguments: []string{"1"}}
		}},
		{"integer unsigned", func(v *ValueEvidence) {
			v.Type.LogicalKind = "integer"
			v.Type.Integer = &compilerir.IntegerTypeFacts{Unsigned: true}
		}},
		{"integer display width set", func(v *ValueEvidence) {
			v.Type.LogicalKind = "integer"
			v.Type.Integer = &compilerir.IntegerTypeFacts{DisplayWidth: compilerir.OptionalInt{Set: true}}
		}},
		{"integer display width value", func(v *ValueEvidence) {
			v.Type.LogicalKind = "integer"
			v.Type.Integer = &compilerir.IntegerTypeFacts{DisplayWidth: compilerir.OptionalInt{Value: 8}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fact := ValueEvidence{Name: "x", Type: TypeEvidence{LogicalKind: "text", Certainty: compilerir.CertaintyKnown}}
			other := fact
			tc.edit(&other)
			_, err := mergeValues([]ValueDeclaration{{Name: "x", Scalar: "text"}}, []ValueEvidence{fact, other}, []string{"x", "x"}, compilerir.MappingConfig{})
			require.ErrorContains(t, err, "conflicting evidence")
		})
	}
}
