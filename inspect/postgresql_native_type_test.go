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
		portable, native, err := postgreSQLNativeColumn(schema.OpaqueType{}, "USER-DEFINED", noNumber, noNumber, noNumber,
			valid("app"), valid("mood"), missing, missing,
			valid("app"), valid("mood"), valid("e"), valid("E"),
			missing, missing, missing, missing, missing, missing, missing, missing,
			valid(`["sad","happy"]`))
		require.NoError(t, err)
		require.Equal(t, schema.OpaqueType{}, portable)
		require.Equal(t, &schema.NativeTypeDef{Dialect: "postgresql", Schema: "app", Name: "mood", Kind: schema.NativeEnum, Arguments: []string{"sad", "happy"}}, native)
	})

	t.Run("unconstrained numeric", func(t *testing.T) {
		portable, native, err := postgreSQLNativeColumn(schema.DecimalType{}, "numeric", noNumber, noNumber, noNumber,
			valid("pg_catalog"), valid("numeric"), missing, missing,
			valid("pg_catalog"), valid("numeric"), valid("b"), valid("N"),
			missing, missing, missing, missing, missing, missing, missing, missing,
			missing)
		require.NoError(t, err)
		require.Equal(t, schema.OpaqueType{}, portable)
		require.Equal(t, &schema.NativeTypeDef{Dialect: "postgresql", Schema: "pg_catalog", Name: "numeric", Kind: schema.NativeBuiltin}, native)
	})

	t.Run("timestamp precision", func(t *testing.T) {
		portable, native, err := postgreSQLNativeColumn(schema.TimeType{}, "timestamp with time zone", noNumber, noNumber, sql.NullInt64{Int64: 3, Valid: true},
			valid("pg_catalog"), valid("timestamptz"), missing, missing,
			valid("pg_catalog"), valid("timestamptz"), valid("b"), valid("D"),
			missing, missing, missing, missing, missing, missing, missing, missing,
			missing)
		require.NoError(t, err)
		require.Equal(t, schema.TimeType{}, portable)
		require.Equal(t, &schema.NativeTypeDef{Dialect: "postgresql", Schema: "pg_catalog", Name: "timestamptz", Kind: schema.NativeBuiltin, Arguments: []string{"3"}}, native)
	})

	t.Run("missing timestamp precision", func(t *testing.T) {
		_, _, err := postgreSQLNativeColumn(schema.TimeType{}, "time", noNumber, noNumber, noNumber,
			valid("pg_catalog"), valid("time"), missing, missing,
			valid("pg_catalog"), valid("time"), valid("b"), valid("D"),
			missing, missing, missing, missing, missing, missing, missing, missing,
			missing)
		require.ErrorContains(t, err, "invalid datetime precision")
	})
}
