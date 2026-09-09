package schemagen_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestColumnGoType is the direct test of schemagen.ColumnGoType, the one
// mapping from a schema column type to a Go type shared by generated row
// fields and generated query parameters. Nullable is never consulted: the
// nine cases below all use non-nullable columns, matching how a generated
// row field and a generated query parameter both consult this mapping
// without regard to Nullable.
func TestColumnGoType(t *testing.T) {
	for _, test := range []struct {
		name   string
		column schema.ColumnDef
		want   string
	}{
		{name: "integer", column: schema.ColumnDef{Name: "id", Type: schema.IntegerType{}}, want: "int64"},
		{name: "unsigned integer", column: schema.ColumnDef{Name: "id", Type: schema.IntegerType{Unsigned: true}}, want: "uint64"},
		{name: "boolean", column: schema.ColumnDef{Name: "active", Type: schema.BooleanType{}}, want: "bool"},
		{name: "float", column: schema.ColumnDef{Name: "amount", Type: schema.FloatType{}}, want: "float64"},
		{name: "text", column: schema.ColumnDef{Name: "email", Type: schema.TextType{}}, want: "string"},
		{name: "uuid", column: schema.ColumnDef{Name: "id", Type: schema.UUIDType{}}, want: "string"},
		{name: "bytes", column: schema.ColumnDef{Name: "payload", Type: schema.BytesType{}}, want: "[]byte"},
		{name: "json", column: schema.ColumnDef{Name: "attributes", Type: schema.JSONType{}}, want: "[]byte"},
		{name: "time", column: schema.ColumnDef{Name: "created_at", Type: schema.TimeType{}}, want: "time.Time"},
		{name: "decimal", column: schema.ColumnDef{Name: "amount", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2)}}, want: "string"},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, schemagen.ColumnGoType(test.column))
		})
	}
}
