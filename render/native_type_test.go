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

func TestCreateTableNativeRendererMatrix(t *testing.T) {
	tests := []struct {
		name    string
		dialect dialect.Dialect
		native  *schema.NativeTypeDef
		want    string
	}{
		{"postgres domain", dialect.PostgreSQL(), &schema.NativeTypeDef{Dialect: "postgresql", Schema: "app", Name: "amount_domain", Kind: schema.NativeDomain}, `CREATE TABLE "events" ("value" "app"."amount_domain" NOT NULL)`},
		{"postgres array", dialect.PostgreSQL(), &schema.NativeTypeDef{Dialect: "postgresql", Schema: "app", Name: "mood", Kind: schema.NativeArray, Element: &schema.NativeTypeDef{Dialect: "postgresql", Schema: "app", Name: "mood", Kind: schema.NativeEnum}}, `CREATE TABLE "events" ("value" "app"."mood"[] NOT NULL)`},
		{"postgres timestamp precision", dialect.PostgreSQL(), &schema.NativeTypeDef{Dialect: "postgresql", Schema: "pg_catalog", Name: "timestamptz", Kind: schema.NativeBuiltin, Arguments: []string{"3"}}, `CREATE TABLE "events" ("value" "pg_catalog"."timestamptz"(3) NOT NULL)`},
		{"postgres timestamp zero precision", dialect.PostgreSQL(), &schema.NativeTypeDef{Dialect: "postgresql", Schema: "pg_catalog", Name: "timestamp", Kind: schema.NativeBuiltin, Arguments: []string{"0"}}, `CREATE TABLE "events" ("value" "pg_catalog"."timestamp"(0) NOT NULL)`},
		{"postgres time precision", dialect.PostgreSQL(), &schema.NativeTypeDef{Dialect: "postgresql", Name: "time", Kind: schema.NativeBuiltin, Arguments: []string{"3"}}, `CREATE TABLE "events" ("value" "time"(3) NOT NULL)`},
		{"postgres timetz precision", dialect.PostgreSQL(), &schema.NativeTypeDef{Dialect: "postgresql", Schema: "pg_catalog", Name: "timetz", Kind: schema.NativeBuiltin, Arguments: []string{"0"}}, `CREATE TABLE "events" ("value" "pg_catalog"."timetz"(0) NOT NULL)`},
		{"mysql escaping", dialect.MySQL(), &schema.NativeTypeDef{Dialect: "mysql", Name: "choice", Kind: schema.NativeEnum, Arguments: []string{"a\\b", "quote's"}}, "CREATE TABLE `events` (`value` ENUM('a\\\\b', 'quote''s') NOT NULL)"},
		{"sqlite declaration", dialect.SQLite(), &schema.NativeTypeDef{Dialect: "sqlite", Name: "VARCHAR", Kind: schema.NativeOther, Arguments: []string{"12"}}, `CREATE TABLE "events" ("value" VARCHAR(12) NOT NULL)`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			statement, err := render.CreateTable(test.dialect, schema.TableDef{Name: "events", Columns: []schema.ColumnDef{{Name: "value", Type: schema.OpaqueType{}, NativeType: test.native}}})
			require.NoError(t, err)
			require.Equal(t, test.want, statement.SQL())
		})
	}
}

func TestCreateTableRejectsInvalidNativeIdentityBeforeSQL(t *testing.T) {
	native := &schema.NativeTypeDef{Dialect: "sqlite", Name: "TEXT; DROP TABLE users", Kind: schema.NativeOther}
	_, err := render.CreateTable(dialect.SQLite(), schema.TableDef{Name: "events", Columns: []schema.ColumnDef{{Name: "value", Type: schema.OpaqueType{}, NativeType: native}}})
	var unsupported *render.ErrUnsupportedNativeType
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "sqlite", unsupported.Dialect)
	require.Equal(t, "events", unsupported.Table)
	require.Equal(t, "value", unsupported.Column)
	require.Equal(t, *native, unsupported.Native)
}

func TestCreateTableRejectsMalformedSQLiteNativeArguments(t *testing.T) {
	for _, native := range []*schema.NativeTypeDef{
		{Dialect: "sqlite", Name: "VARCHAR", Kind: schema.NativeOther, Arguments: []string{"12x"}},
		{Dialect: "sqlite", Name: "VARCHAR", Kind: schema.NativeOther, Arguments: []string{""}},
		{Dialect: "sqlite", Name: "VARCHAR;DROP", Kind: schema.NativeOther},
	} {
		_, err := render.CreateTable(dialect.SQLite(), schema.TableDef{Name: "events", Columns: []schema.ColumnDef{{Name: "value", Type: schema.OpaqueType{}, NativeType: native}}})
		var unsupported *render.ErrUnsupportedNativeType
		require.ErrorAs(t, err, &unsupported)
		require.Equal(t, "events", unsupported.Table)
	}
}
