package schema_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestNativeTypeDescriptorRoundTripAndClone(t *testing.T) {
	descriptor := schema.TableDef{
		Name: "events",
		Columns: []schema.ColumnDef{{
			Name:       "status",
			Type:       schema.OpaqueType{},
			NativeType: &schema.NativeTypeDef{Dialect: "postgresql", Schema: "public", Name: "status", Kind: schema.NativeEnum, Arguments: []string{"new", "it's done"}},
		}},
	}
	require.NoError(t, descriptor.Validate())
	clone := descriptor.Clone()
	require.True(t, reflect.DeepEqual(descriptor, clone))
	clone.Columns[0].NativeType.Arguments[0] = "changed"
	require.Equal(t, "new", descriptor.Columns[0].NativeType.Arguments[0])
	payload, err := json.Marshal(descriptor)
	require.NoError(t, err)
	var decoded schema.TableDef
	require.NoError(t, json.Unmarshal(payload, &decoded))
	require.True(t, reflect.DeepEqual(descriptor, decoded))
}

func TestNativeTypeValidationRejectsInvalidShapes(t *testing.T) {
	tests := []schema.TableDef{
		{Name: "events", Columns: []schema.ColumnDef{{Name: "value", Type: schema.OpaqueType{}}}},
		{Name: "events", Columns: []schema.ColumnDef{{Name: "value", Type: schema.TextType{}, NativeType: &schema.NativeTypeDef{Dialect: "sqlite", Name: "text", Kind: schema.NativeOther, Element: &schema.NativeTypeDef{Dialect: "sqlite", Name: "text", Kind: schema.NativeOther}}}}},
		{Name: "events", Columns: []schema.ColumnDef{{Name: "value", Type: schema.OpaqueType{}, NativeType: &schema.NativeTypeDef{Dialect: "postgresql", Name: "array", Kind: schema.NativeArray}}}},
	}
	for _, descriptor := range tests {
		require.Error(t, descriptor.Validate())
	}
	array := &schema.NativeTypeDef{Dialect: "postgresql", Name: "outer", Kind: schema.NativeArray}
	current := array
	for range 32 {
		current.Element = &schema.NativeTypeDef{Dialect: "postgresql", Name: "nested", Kind: schema.NativeArray}
		current = current.Element
	}
	current.Element = &schema.NativeTypeDef{Dialect: "postgresql", Name: "too_deep", Kind: schema.NativeArray, Element: &schema.NativeTypeDef{Dialect: "postgresql", Name: "leaf", Kind: schema.NativeOther}}
	err := (schema.TableDef{Name: "events", Columns: []schema.ColumnDef{{Name: "value", Type: schema.OpaqueType{}, NativeType: array}}}).Validate()
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "depth"))
}

func TestNativeTypeValidationMatrix(t *testing.T) {
	for _, kind := range []schema.NativeTypeKind{schema.NativeBuiltin, schema.NativeDomain, schema.NativeEnum, schema.NativeSet, schema.NativeArray, schema.NativeOther} {
		dialect := "postgresql"
		if kind == schema.NativeSet {
			dialect = "mysql"
		}
		native := &schema.NativeTypeDef{Dialect: dialect, Name: "value", Kind: kind}
		if kind == schema.NativeArray {
			native.Element = &schema.NativeTypeDef{Dialect: dialect, Name: "value", Kind: schema.NativeOther}
		}
		require.NoError(t, (schema.TableDef{Name: "events", Columns: []schema.ColumnDef{{Name: "value", Type: schema.OpaqueType{}, NativeType: native}}}).Validate(), string(kind))
	}
	for _, native := range []*schema.NativeTypeDef{
		{Dialect: "postgresql", Name: "value", Kind: schema.NativeEnum, Element: &schema.NativeTypeDef{Dialect: "postgresql", Name: "other", Kind: schema.NativeOther}},
		{Dialect: "postgresql", Name: "bad.name", Kind: schema.NativeOther},
		{Dialect: "postgresql", Name: "value", Kind: schema.NativeTypeKind("unknown")},
	} {
		require.Error(t, (schema.TableDef{Name: "events", Columns: []schema.ColumnDef{{Name: "value", Type: schema.OpaqueType{}, NativeType: native}}}).Validate())
	}
	for _, native := range []*schema.NativeTypeDef{
		{Kind: schema.NativeOther, Name: "value"},
		{Dialect: "postgresql", Kind: schema.NativeOther},
		{Dialect: "postgresql", Name: "value"},
	} {
		require.Error(t, (schema.TableDef{Name: "events", Columns: []schema.ColumnDef{{Name: "value", Type: schema.OpaqueType{}, NativeType: native}}}).Validate())
	}
	validDepth := &schema.NativeTypeDef{Dialect: "postgresql", Name: "value", Kind: schema.NativeArray}
	current := validDepth
	for index := 1; index < 31; index++ {
		current.Element = &schema.NativeTypeDef{Dialect: "postgresql", Name: "value", Kind: schema.NativeArray}
		current = current.Element
	}
	current.Element = &schema.NativeTypeDef{Dialect: "postgresql", Name: "leaf", Kind: schema.NativeOther}
	descriptor := schema.TableDef{Name: "events", Columns: []schema.ColumnDef{{Name: "value", Type: schema.OpaqueType{}, NativeType: validDepth}}}
	require.NoError(t, descriptor.Validate())
	clone := descriptor.Clone()
	clone.Columns[0].NativeType.Element.Name = "changed"
	require.Equal(t, "value", descriptor.Columns[0].NativeType.Element.Name)
	empty := schema.NativeTypeDef{Dialect: "postgresql", Name: "value", Kind: schema.NativeEnum, Arguments: []string{}}
	nilArguments := schema.NativeTypeDef{Dialect: "postgresql", Name: "value", Kind: schema.NativeEnum}
	emptyJSON, err := json.Marshal(empty)
	require.NoError(t, err)
	nilJSON, err := json.Marshal(nilArguments)
	require.NoError(t, err)
	require.NotEqual(t, string(emptyJSON), string(nilJSON))
}
