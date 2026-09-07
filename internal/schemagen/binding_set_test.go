package schemagen

import (
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type testPackageNames map[string]string

func (m testPackageNames) Name(_ string, path string) (string, error) { return m[path], nil }

func bindingSetColumn(expression string, imports ...schema.GoImport) schema.ColumnDef {
	return schema.ColumnDef{Name: "value", Type: schema.TextType{}, GoBinding: &schema.GoBinding{Type: expression, Imports: imports}}
}

func TestBindingSetCanonicalIdentityAndAliases(t *testing.T) {
	set := NewBindingSet(BindingSetOptions{Reserved: []string{"time", "stmt", "Run", "value"}, resolver: testPackageNames{"example.com/types/v2": "types"}})
	left, err := set.Add(bindingSetColumn("u.URL", schema.GoImport{Path: "net/url", Name: "u"}))
	require.NoError(t, err)
	right, err := set.Add(bindingSetColumn("url.URL", schema.GoImport{Path: "net/url", Name: "url"}))
	require.NoError(t, err)
	require.True(t, SameBindingType(left, right, false))
	versionedSet := NewBindingSet(BindingSetOptions{resolver: testPackageNames{"example.com/types/v2": "types"}})
	versioned, err := versionedSet.Add(bindingSetColumn("types.ID", schema.GoImport{Path: "example.com/types/v2"}))
	require.NoError(t, err)
	require.False(t, SameBindingType(left, versioned, false))
	require.Error(t, set.Finalize())
	require.NoError(t, versionedSet.Finalize())
	require.Error(t, versionedSet.Finalize())
	rendered, err := versionedSet.Type(versioned, false)
	require.NoError(t, err)
	require.Equal(t, "types.ID", rendered)
	imports := versionedSet.Imports()
	imports[0].Name = "mutated"
	require.NotEqual(t, "mutated", versionedSet.Imports()[0].Name)
}

func TestBindingSetRejectsConflictingAliasesAtFinalize(t *testing.T) {
	set := NewBindingSet(BindingSetOptions{})
	_, err := set.Add(bindingSetColumn("u.URL", schema.GoImport{Path: "net/url", Name: "u"}))
	require.NoError(t, err)
	_, err = set.Add(bindingSetColumn("url.URL", schema.GoImport{Path: "net/url", Name: "url"}))
	require.NoError(t, err)
	require.ErrorContains(t, set.Finalize(), `net/url`)
	require.ErrorContains(t, set.Finalize(), `u`)
}

func TestBindingSetAssignsAliasesByPathAndReservedNames(t *testing.T) {
	set := NewBindingSet(BindingSetOptions{Reserved: []string{"x", "x2"}})
	a, err := set.Add(bindingSetColumn("x.URL", schema.GoImport{Path: "html/template", Name: "x"}))
	require.NoError(t, err)
	b, err := set.Add(bindingSetColumn("x.URL", schema.GoImport{Path: "net/url", Name: "x"}))
	require.NoError(t, err)
	require.NoError(t, set.Finalize())
	renderedA, err := set.Type(a, false)
	require.NoError(t, err)
	renderedB, err := set.Type(b, false)
	require.NoError(t, err)
	require.Equal(t, "x3.URL", renderedA)
	require.Equal(t, "x4.URL", renderedB)
	require.Equal(t, "html/template", set.Imports()[0].Path)
}
