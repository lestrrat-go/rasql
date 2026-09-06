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

func TestGeneratedBindingDefaultsRemainUnchanged(t *testing.T) {
	table := schema.TableDef{Name: "defaults", Columns: []schema.ColumnDef{
		{Name: "value", Type: schema.TextType{}, Nullable: true},
		{Name: "payload", Type: schema.JSONType{}, Nullable: true},
	}}
	source, err := schemagen.PackageSource("generated", table)
	require.NoError(t, err)
	text := string(source)
	require.Contains(t, text, "Value   *string")
	require.Contains(t, text, "Payload []byte")
}

func TestGeneratedBindingAliasCollisionIsRewritten(t *testing.T) {
	table := schema.TableDef{Name: "events", Columns: []schema.ColumnDef{{
		Name: "payload", Type: schema.TextType{}, GoBinding: &schema.GoBinding{
			Type: "time.Time", Imports: []schema.GoImport{{Path: "time"}},
		},
	}}}
	source, err := schemagen.PackageSource("generated", table)
	require.NoError(t, err)
	require.Contains(t, string(source), `time2 "time"`)
	require.Contains(t, string(source), "time2.Time")
}

func TestRelationshipRejectsIncompatibleResolvedBindings(t *testing.T) {
	parent := schema.TableDef{Name: "users", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{
		Name: "id", Type: schema.TextType{}, GoBinding: &schema.GoBinding{Type: "UserID"},
	}}}
	child := schema.TableDef{Name: "orders", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "user_id", Type: schema.TextType{}, GoBinding: &schema.GoBinding{Type: "OtherID"}},
	}, ForeignKeys: []schema.ForeignKeyDef{{
		Name: "orders_user_fk", Columns: []string{"user_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"},
	}}, Relationships: []schema.RelationshipDef{{
		Name: "user", Kind: schema.RelationshipBelongsTo, Columns: []string{"user_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"},
	}}}
	_, err := schemagen.PackageSource("generated", parent, child)
	require.NoError(t, err)
	// The relationship is deliberately unsupported and therefore emits no Load method.
	require.NotContains(t, stringMustSource(t, parent, child), "OrdersTableUserRelation")
}

func TestRelationshipUsesCanonicalImportedBindingAndOutputAlias(t *testing.T) {
	parent := schema.TableDef{Name: "users", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{
		Name: "id", Type: schema.TextType{}, GoBinding: &schema.GoBinding{Type: "u.URL", Imports: []schema.GoImport{{Path: "net/url", Name: "u"}}},
	}}}
	child := schema.TableDef{Name: "orders", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "user_id", Type: schema.TextType{}, GoBinding: &schema.GoBinding{Type: "url.URL", Imports: []schema.GoImport{{Path: "net/url", Name: "url"}}}},
	}, ForeignKeys: []schema.ForeignKeyDef{{Columns: []string{"user_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}}}, Relationships: []schema.RelationshipDef{{Name: "user", Kind: schema.RelationshipBelongsTo, Columns: []string{"user_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}}}}
	source, err := schemagen.PackageSource("generated", parent, child)
	require.NoError(t, err)
	text := string(source)
	require.Contains(t, text, `url "net/url"`)
	require.Contains(t, text, "map[url.URL]")
	require.Contains(t, text, "type OrdersTableUserRelation")
}

func TestRelationshipRejectsSameSelectorFromDifferentImportPaths(t *testing.T) {
	parent := schema.TableDef{Name: "users", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{
		Name: "id", Type: schema.TextType{}, GoBinding: &schema.GoBinding{Type: "x.URL", Imports: []schema.GoImport{{Path: "net/url", Name: "x"}}},
	}}}
	child := schema.TableDef{Name: "orders", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "user_id", Type: schema.TextType{}, GoBinding: &schema.GoBinding{Type: "x.URL", Imports: []schema.GoImport{{Path: "html/template", Name: "x"}}}},
	}, ForeignKeys: []schema.ForeignKeyDef{{Columns: []string{"user_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}}}, Relationships: []schema.RelationshipDef{{Name: "user", Kind: schema.RelationshipBelongsTo, Columns: []string{"user_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}}}}
	source, err := schemagen.PackageSource("generated", parent, child)
	require.NoError(t, err)
	require.NotContains(t, string(source), "OrdersTableUserRelation")
}

func stringMustSource(t *testing.T, tables ...schema.TableDef) string {
	t.Helper()
	source, err := schemagen.PackageSource("generated", tables...)
	require.NoError(t, err)
	return string(source)
}
