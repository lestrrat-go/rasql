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
