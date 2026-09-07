package compilerlock_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/stretchr/testify/require"
)

func evidenceFixture(t *testing.T) compilerlock.File {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "v1", "postgresql.json"))
	require.NoError(t, err)
	f, err := compilerlock.Decode(b)
	require.NoError(t, err)
	return f
}

func TestEvidencePublicEncodeDecodePreservesCompleteFacts(t *testing.T) {
	f := evidenceFixture(t)
	q := &f.Queries[0]
	native := &compilerlock.NativeTypeRecord{Dialect: "postgresql", Schema: "public", Name: "text", Kind: "builtin", Arguments: func() *[]string { v := []string{"12"}; return &v }(), Element: &compilerlock.NativeTypeRecord{Dialect: "postgresql", Name: "text", Kind: "builtin"}}
	q.Parameters[0] = compilerlock.ValueRecord{Name: "value", Scalar: "text", Nullable: true, TypeCertainty: compilerir.CertaintyKnown, NullabilityCertainty: compilerir.CertaintyKnown, LogicalKind: "text", Native: native}
	q.Results[0] = compilerlock.ValueRecord{Name: "value", Scalar: "integer", TypeCertainty: compilerir.CertaintyDeclared, NullabilityCertainty: compilerir.CertaintyDeclared, LogicalKind: "integer", Integer: &compilerlock.IntegerTypeFactsRecord{Unsigned: true, DisplayWidth: compilerlock.OptionalIntRecord{Set: true, Value: 11}}}
	q.Evidence.Parameters = append([]compilerlock.ValueRecord(nil), q.Parameters...)
	q.Evidence.Results = append([]compilerlock.ValueRecord(nil), q.Results...)

	encoded, err := compilerlock.Encode(f)
	require.NoError(t, err)
	decoded, err := compilerlock.Decode(encoded)
	require.NoError(t, err)
	require.True(t, reflect.DeepEqual(q.Parameters, decoded.Queries[0].Parameters))
	require.True(t, reflect.DeepEqual(q.Results, decoded.Queries[0].Results))
	require.NotNil(t, decoded.Queries[0].Parameters[0].Native.Element)
}

func TestEvidenceCloneDeepCopiesParameterAndResultFacts(t *testing.T) {
	model := compilerir.QueryAnalysis{
		Parameters: []compilerir.SemanticValue{{Name: "p", Scalar: "text", Native: &compilerir.NativeType{Dialect: "postgresql", Name: "array", Kind: "array", Arguments: []string{"1"}, Element: &compilerir.NativeType{Dialect: "postgresql", Name: "text", Kind: "builtin"}}}},
		Results:    []compilerir.SemanticValue{{Name: "r", Scalar: "integer", Integer: &compilerir.IntegerTypeFacts{DisplayWidth: compilerir.OptionalInt{Set: true, Value: 8}}}},
	}
	clone := model.Clone()
	clone.Parameters[0].Native.Arguments[0] = "2"
	clone.Parameters[0].Native.Element.Name = "changed"
	clone.Results[0].Integer.DisplayWidth.Value = 4
	require.Equal(t, "1", model.Parameters[0].Native.Arguments[0])
	require.Equal(t, "text", model.Parameters[0].Native.Element.Name)
	require.Equal(t, 8, model.Results[0].Integer.DisplayWidth.Value)
}

func TestEvidenceEncodeReportsTypedValidationPaths(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*compilerlock.File)
		want   string
	}{
		{"integer facts require integer logical kind", func(f *compilerlock.File) {
			f.Queries[0].Parameters[0].Integer = &compilerlock.IntegerTypeFactsRecord{}
			f.Queries[0].Evidence.Parameters = append([]compilerlock.ValueRecord(nil), f.Queries[0].Parameters...)
		}, "integer facts"},
		{"integer display width cannot be negative", func(f *compilerlock.File) {
			v := &f.Queries[0].Parameters[0]
			v.LogicalKind = "integer"
			v.Scalar = "integer"
			v.Integer = &compilerlock.IntegerTypeFactsRecord{DisplayWidth: compilerlock.OptionalIntRecord{Set: true, Value: -1}}
			f.Queries[0].Evidence.Parameters = append([]compilerlock.ValueRecord(nil), f.Queries[0].Parameters...)
		}, "display width"},
		{"native dialect is checked", func(f *compilerlock.File) {
			f.Queries[0].Parameters[0].Native = &compilerlock.NativeTypeRecord{Dialect: "oracle", Name: "text", Kind: "builtin"}
			f.Queries[0].Evidence.Parameters = append([]compilerlock.ValueRecord(nil), f.Queries[0].Parameters...)
		}, "unsupported dialect"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := evidenceFixture(t)
			tc.mutate(&f)
			_, err := compilerlock.Encode(f)
			require.Error(t, err)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestEvidenceLegacyBuiltInFactsRemainReadable(t *testing.T) {
	f := evidenceFixture(t)
	q := &f.Queries[1]
	q.Parameters[0].LogicalKind = ""
	q.Parameters[0].Native = nil
	q.Parameters[0].Integer = nil
	q.Results[0].LogicalKind = ""
	q.Results[0].Native = nil
	q.Results[0].Integer = nil
	q.Evidence.Parameters = append([]compilerlock.ValueRecord(nil), q.Parameters...)
	q.Evidence.Results = append([]compilerlock.ValueRecord(nil), q.Results...)
	encoded, err := compilerlock.Encode(f)
	require.NoError(t, err)
	_, err = compilerlock.Decode(encoded)
	require.NoError(t, err)
}

func TestEvidenceDigestChangesForEveryFact(t *testing.T) {
	baseValue := compilerlock.ValueRecord{Name: "p", Scalar: "text", TypeCertainty: compilerir.CertaintyKnown, NullabilityCertainty: compilerir.CertaintyKnown, LogicalKind: "text", Native: &compilerlock.NativeTypeRecord{Dialect: "postgresql", Schema: "public", Name: "text", Kind: "builtin", Arguments: func() *[]string { v := []string{"1"}; return &v }(), Element: &compilerlock.NativeTypeRecord{Dialect: "postgresql", Name: "text", Kind: "builtin"}}}
	base := compilerlock.DigestInputs{Source: compilerlock.SourceDigestInput{Record: compilerlock.SourceRecord{Kind: "live", Identity: "schema"}, Engine: compilerlock.EngineRecord{Dialect: "postgresql", Profile: "postgresql-16"}}, Queries: []compilerlock.QueryDigestInput{{ID: "q", SQL: compilerlock.SourceFile{Path: "q.sql", SHA256: strings.Repeat("a", 64)}, Operation: "select", Parameters: []compilerlock.ValueRecord{baseValue}, Results: []compilerlock.ValueRecord{{Name: "n", Scalar: "integer", LogicalKind: "integer", TypeCertainty: compilerir.CertaintyDeclared, NullabilityCertainty: compilerir.CertaintyDeclared, Integer: &compilerlock.IntegerTypeFactsRecord{}}}, Cardinality: "many"}}, Generation: compilerir.GoConfig{Package: "p", Output: "out", Emitter: "compact"}}
	want, err := compilerlock.BuildDigests(base)
	require.NoError(t, err)
	cases := []struct {
		name   string
		mutate func(*compilerlock.DigestInputs)
	}{
		{"name", func(in *compilerlock.DigestInputs) { in.Queries[0].Parameters[0].Name = "other" }},
		{"scalar", func(in *compilerlock.DigestInputs) { in.Queries[0].Parameters[0].Scalar = "other" }},
		{"nullable", func(in *compilerlock.DigestInputs) { in.Queries[0].Parameters[0].Nullable = true }},
		{"type certainty", func(in *compilerlock.DigestInputs) {
			in.Queries[0].Parameters[0].TypeCertainty = compilerir.CertaintyDeclared
		}},
		{"nullability certainty", func(in *compilerlock.DigestInputs) {
			in.Queries[0].Parameters[0].NullabilityCertainty = compilerir.CertaintyDeclared
		}},
		{"logical kind", func(in *compilerlock.DigestInputs) { in.Queries[0].Parameters[0].LogicalKind = "varchar" }},
		{"native schema", func(in *compilerlock.DigestInputs) { in.Queries[0].Parameters[0].Native.Schema = "other" }},
		{"native dialect", func(in *compilerlock.DigestInputs) { in.Queries[0].Parameters[0].Native.Dialect = "sqlite" }},
		{"native name", func(in *compilerlock.DigestInputs) { in.Queries[0].Parameters[0].Native.Name = "varchar" }},
		{"native kind", func(in *compilerlock.DigestInputs) { in.Queries[0].Parameters[0].Native.Kind = "domain" }},
		{"native argument", func(in *compilerlock.DigestInputs) { (*in.Queries[0].Parameters[0].Native.Arguments)[0] = "2" }},
		{"native element", func(in *compilerlock.DigestInputs) { in.Queries[0].Parameters[0].Native.Element.Name = "varchar" }},
		{"integer unsigned", func(in *compilerlock.DigestInputs) { in.Queries[0].Results[0].Integer.Unsigned = true }},
		{"integer width", func(in *compilerlock.DigestInputs) {
			in.Queries[0].Results[0].Integer.DisplayWidth = compilerlock.OptionalIntRecord{Set: true, Value: 8}
		}},
		{"integer zero fill", func(in *compilerlock.DigestInputs) { in.Queries[0].Results[0].Integer.ZeroFill = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := base
			candidate.Queries = append([]compilerlock.QueryDigestInput(nil), base.Queries...)
			candidate.Queries[0].Parameters = append([]compilerlock.ValueRecord(nil), base.Queries[0].Parameters...)
			candidate.Queries[0].Results = append([]compilerlock.ValueRecord(nil), base.Queries[0].Results...)
			candidate.Queries[0].Parameters[0].Native = &compilerlock.NativeTypeRecord{Dialect: "postgresql", Schema: "public", Name: "text", Kind: "builtin", Arguments: func() *[]string { v := []string{"1"}; return &v }(), Element: &compilerlock.NativeTypeRecord{Dialect: "postgresql", Name: "text", Kind: "builtin"}}
			tc.mutate(&candidate)
			got, err := compilerlock.BuildDigests(candidate)
			require.NoError(t, err)
			require.NotEqual(t, want.Queries, got.Queries)
		})
	}
}
