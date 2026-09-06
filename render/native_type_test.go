package render_test

import (
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestCreateTableRendersNativeTypesOnlyOnTheirDialect(t *testing.T) {
	postgresTable := schema.TableDef{
		Name:    "events",
		Columns: []schema.ColumnDef{{Name: "status", Type: schema.OpaqueType{}, NativeType: &schema.NativeTypeDef{Dialect: "postgresql", Schema: "public", Name: "status", Kind: schema.NativeEnum}}},
	}
	statement, err := render.CreateTable(dialect.PostgreSQL(), postgresTable)
	require.NoError(t, err)
	require.Equal(t, `CREATE TABLE "events" ("status" "public"."status" NOT NULL)`, statement.SQL())
	_, err = render.CreateTable(dialect.MySQL(), postgresTable)
	var unsupported *render.ErrUnsupportedNativeType
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "mysql", unsupported.Dialect)

	mySQLTable := schema.TableDef{
		Name:    "users",
		Columns: []schema.ColumnDef{{Name: "role", Type: schema.OpaqueType{}, NativeType: &schema.NativeTypeDef{Dialect: "mysql", Name: "role", Kind: schema.NativeSet, Arguments: []string{"admin", "user's choice"}}}},
	}
	statement, err = render.CreateTable(dialect.MySQL(), mySQLTable)
	require.NoError(t, err)
	require.Equal(t, "CREATE TABLE `users` (`role` SET('admin', 'user''s choice') NOT NULL)", statement.SQL())
	_, err = render.CreateTable(dialect.SQLite(), mySQLTable)
	require.True(t, errors.As(err, &unsupported))
}
