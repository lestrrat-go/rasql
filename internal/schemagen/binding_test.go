package schemagen_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func bindingColumn(binding *schema.GoBinding) schema.ColumnDef {
	return schema.ColumnDef{Name: "value", Type: schema.TextType{}, GoBinding: binding}
}

func TestResolveGoBindingRejectsInvalidImportsAndExpressions(t *testing.T) {
	tests := []struct {
		name    string
		binding *schema.GoBinding
		want    string
	}{
		{"malformed", &schema.GoBinding{Type: "[]"}, "does not parse"},
		{"missing import", &schema.GoBinding{Type: "missing.Type"}, "no matching import"},
		{"unused import", &schema.GoBinding{Type: "string", Imports: []schema.GoImport{{Path: "net/url"}}}, "unused"},
		{"blank path", &schema.GoBinding{Type: "string", Imports: []schema.GoImport{{Path: " "}}}, "blank"},
		{"blank import", &schema.GoBinding{Type: "x.Type", Imports: []schema.GoImport{{Path: "x", Name: "_"}}}, "unsupported"},
		{"duplicate path", &schema.GoBinding{Type: "x.Type", Imports: []schema.GoImport{{Path: "x", Name: "x"}, {Path: "x", Name: "y"}}}, "duplicate import path"},
		{"duplicate alias", &schema.GoBinding{Type: "x.Type", Imports: []schema.GoImport{{Path: "x", Name: "x"}, {Path: "y", Name: "x"}}}, "duplicate import name"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := schemagen.ResolveGoBinding(bindingColumn(test.binding))
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestResolveGoBindingPreservesNullableDefaultsAndExplicitTypes(t *testing.T) {
	jsonColumn := schema.ColumnDef{Name: "value", Type: schema.JSONType{}, Nullable: true}
	resolved, err := schemagen.ResolveGoBinding(jsonColumn)
	require.NoError(t, err)
	require.Equal(t, "[]byte", resolved.For(true))

	custom := bindingColumn(&schema.GoBinding{Type: "UserID", NullableType: "NullUserID"})
	custom.Nullable = true
	resolved, err = schemagen.ResolveGoBinding(custom)
	require.NoError(t, err)
	require.Equal(t, "NullUserID", resolved.For(true))
}
