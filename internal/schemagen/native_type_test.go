package schemagen_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestPackageSourceEmitsNativeTypeAndOpaqueType(t *testing.T) {
	source, err := schemagen.PackageSource("generated", schema.TableDef{
		Name: "events",
		Columns: []schema.ColumnDef{{Name: "status", Type: schema.OpaqueType{}, NativeType: &schema.NativeTypeDef{
			Dialect: "postgresql", Schema: "public", Name: "status", Kind: schema.NativeEnum,
		}}},
	})
	require.NoError(t, err)
	text := string(source)
	require.True(t, strings.Contains(text, "schema.OpaqueType{}"))
	require.True(t, strings.Contains(text, "NativeType: &schema.NativeTypeDef"))
	require.True(t, strings.Contains(text, `Name: "status"`))
}

func TestNativeTypeGenerationKeepsOpaqueRuntimeBindingAndNestedValues(t *testing.T) {
	native := &schema.NativeTypeDef{Dialect: "postgresql", Schema: "app", Name: "mood", Kind: schema.NativeEnum, Arguments: []string{}}
	table := schema.TableDef{Name: "events", Columns: []schema.ColumnDef{{Name: "mood", Type: schema.OpaqueType{}, NativeType: native}}}
	require.Equal(t, "any", schemagen.ColumnGoType(table.Columns[0]))
	packageSource, err := schemagen.PackageSource("generated", table)
	require.NoError(t, err)
	tableSource, err := schemagen.TableSource("generated", table)
	require.NoError(t, err)
	descriptorSource, err := schemagen.DescriptorSource("generated", table)
	require.NoError(t, err)
	for _, source := range [][]byte{packageSource, tableSource, descriptorSource} {
		require.Contains(t, string(source), `Arguments: []string{}`)
		require.Contains(t, string(source), `Name: "mood"`)
	}
	require.Contains(t, string(tableSource), "func (r *EventsRow) ScanRow(src rasql.ScanSource) error")
	require.Contains(t, string(tableSource), "func (r *EventsRow) ScanDestinations(columns []string) ([]any, error)")
	require.Contains(t, string(tableSource), "func (r EventsRow) ColumnValue(name string) (any, bool)")
	encoded, err := json.Marshal(native)
	require.NoError(t, err)
	var decoded schema.NativeTypeDef
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.NotNil(t, decoded.Arguments)
	require.Empty(t, decoded.Arguments)
	_, err = schemagen.PackageSource("generated", schema.TableDef{Name: "broken", Columns: []schema.ColumnDef{{Name: "value", Type: schema.OpaqueType{}}}})
	require.Error(t, err)
}
