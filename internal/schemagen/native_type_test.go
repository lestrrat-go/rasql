package schemagen_test

import (
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
