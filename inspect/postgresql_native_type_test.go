package inspect

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestPostgreSQLNativeColumnMapping(t *testing.T) {
	valid := func(value string) sql.NullString { return sql.NullString{String: value, Valid: true} }
	missing := sql.NullString{}
	noNumber := sql.NullInt64{}

	t.Run("enum", func(t *testing.T) {
		portable, native, err := postgreSQLNativeColumn(schema.OpaqueType{}, "USER-DEFINED", noNumber, noNumber,
			valid("app"), valid("mood"), missing, missing,
			valid("app"), valid("mood"), valid("e"), valid("E"),
			missing, missing, missing, missing, missing, missing, missing, missing,
			valid(`["sad","happy"]`))
		require.NoError(t, err)
		require.Equal(t, schema.OpaqueType{}, portable)
		require.Equal(t, &schema.NativeTypeDef{Dialect: "postgresql", Schema: "app", Name: "mood", Kind: schema.NativeEnum, Arguments: []string{"sad", "happy"}}, native)
	})

	t.Run("unconstrained numeric", func(t *testing.T) {
		portable, native, err := postgreSQLNativeColumn(schema.DecimalType{}, "numeric", noNumber, noNumber,
			valid("pg_catalog"), valid("numeric"), missing, missing,
			valid("pg_catalog"), valid("numeric"), valid("b"), valid("N"),
			missing, missing, missing, missing, missing, missing, missing, missing,
			missing)
		require.NoError(t, err)
		require.Equal(t, schema.OpaqueType{}, portable)
		require.Equal(t, &schema.NativeTypeDef{Dialect: "postgresql", Schema: "pg_catalog", Name: "numeric", Kind: schema.NativeBuiltin}, native)
	})
}
