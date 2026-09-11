package schema_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestValidateColumnTypeRejectsPointer states the shape ValidateColumnType
// refuses. A pointer to a built-in type satisfies schema.ColumnType, because
// every built-in declares its methods on a value receiver, so a caller can
// write &schema.IntegerType{} and have it compile; this is what stops such a
// value reaching a descriptor that TableDef.Validate, ColumnDef.MarshalJSON
// and every dialect refuse later.
func TestValidateColumnTypeRejectsPointer(t *testing.T) {
	t.Run("pointer to a built-in type", func(t *testing.T) {
		err := schema.ValidateColumnType(&schema.IntegerType{})
		require.EqualError(t, err, "unsupported column type *schema.IntegerType")
	})

	t.Run("nil pointer to a built-in type", func(t *testing.T) {
		err := schema.ValidateColumnType((*schema.IntegerType)(nil))
		require.EqualError(t, err, "unsupported column type *schema.IntegerType")
	})

	t.Run("the value form is accepted", func(t *testing.T) {
		require.NoError(t, schema.ValidateColumnType(schema.IntegerType{}))
	})
}
