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
		{"postgres nested array", dialect.PostgreSQL(), &schema.NativeTypeDef{Dialect: "postgresql", Schema: "app", Name: "moods", Kind: schema.NativeArray, Element: &schema.NativeTypeDef{Dialect: "postgresql", Schema: "app", Name: "moods", Kind: schema.NativeArray, Element: &schema.NativeTypeDef{Dialect: "postgresql", Schema: "app", Name: "mood", Kind: schema.NativeEnum}}}, `CREATE TABLE "events" ("value" "app"."mood"[][] NOT NULL)`},
		{"postgres ordinary type", dialect.PostgreSQL(), &schema.NativeTypeDef{Dialect: "postgresql", Schema: "app", Name: "money_type", Kind: schema.NativeOther}, `CREATE TABLE "events" ("value" "app"."money_type" NOT NULL)`},
		{"postgres timestamp precision", dialect.PostgreSQL(), &schema.NativeTypeDef{Dialect: "postgresql", Schema: "pg_catalog", Name: "timestamptz", Kind: schema.NativeBuiltin, Arguments: []string{"3"}}, `CREATE TABLE "events" ("value" "pg_catalog"."timestamptz"(3) NOT NULL)`},
		{"postgres timestamp zero precision", dialect.PostgreSQL(), &schema.NativeTypeDef{Dialect: "postgresql", Schema: "pg_catalog", Name: "timestamp", Kind: schema.NativeBuiltin, Arguments: []string{"0"}}, `CREATE TABLE "events" ("value" "pg_catalog"."timestamp"(0) NOT NULL)`},
		{"postgres time precision", dialect.PostgreSQL(), &schema.NativeTypeDef{Dialect: "postgresql", Name: "time", Kind: schema.NativeBuiltin, Arguments: []string{"3"}}, `CREATE TABLE "events" ("value" "time"(3) NOT NULL)`},
		{"postgres timetz precision", dialect.PostgreSQL(), &schema.NativeTypeDef{Dialect: "postgresql", Schema: "pg_catalog", Name: "timetz", Kind: schema.NativeBuiltin, Arguments: []string{"0"}}, `CREATE TABLE "events" ("value" "pg_catalog"."timetz"(0) NOT NULL)`},
		{"mysql escaping", dialect.MySQL(), &schema.NativeTypeDef{Dialect: "mysql", Name: "choice", Kind: schema.NativeEnum, Arguments: []string{"a\\b", "quote's"}}, "CREATE TABLE `events` (`value` ENUM('a\\\\b', 'quote''s') NOT NULL)"},
		{"mysql all labels", dialect.MySQL(), &schema.NativeTypeDef{Dialect: "mysql", Name: "choice", Kind: schema.NativeEnum, Arguments: []string{"a,b", "quote's", `quote"`, `back\\slash`, " spaced ", ""}}, "CREATE TABLE `events` (`value` ENUM('a,b', 'quote''s', 'quote\"', 'back\\\\\\\\slash', ' spaced ', '') NOT NULL)"},
		{"mysql set", dialect.MySQL(), &schema.NativeTypeDef{Dialect: "mysql", Name: "flags", Kind: schema.NativeSet, Arguments: []string{"a,b", "quote's", `quote"`, `back\\slash`, " spaced ", ""}}, "CREATE TABLE `events` (`value` SET('a,b', 'quote''s', 'quote\"', 'back\\\\\\\\slash', ' spaced ', '') NOT NULL)"},
		{"sqlite declaration", dialect.SQLite(), &schema.NativeTypeDef{Dialect: "sqlite", Name: "VARCHAR", Kind: schema.NativeOther, Arguments: []string{"12"}}, `CREATE TABLE "events" ("value" VARCHAR(12) NOT NULL)`},
		{"sqlite int", dialect.SQLite(), &schema.NativeTypeDef{Dialect: "sqlite", Name: "INT", Kind: schema.NativeOther}, `CREATE TABLE "events" ("value" INT NOT NULL)`},
		{"sqlite weird type", dialect.SQLite(), &schema.NativeTypeDef{Dialect: "sqlite", Name: "WEIRD_TYPE", Kind: schema.NativeOther}, `CREATE TABLE "events" ("value" "WEIRD_TYPE" NOT NULL)`},
		{"sqlite char", dialect.SQLite(), &schema.NativeTypeDef{Dialect: "sqlite", Name: "CHAR", Kind: schema.NativeOther, Arguments: []string{"4"}}, `CREATE TABLE "events" ("value" CHAR(4) NOT NULL)`},
		{"sqlite float", dialect.SQLite(), &schema.NativeTypeDef{Dialect: "sqlite", Name: "FLOAT", Kind: schema.NativeOther}, `CREATE TABLE "events" ("value" FLOAT NOT NULL)`},
		{"sqlite custom type", dialect.SQLite(), &schema.NativeTypeDef{Dialect: "sqlite", Name: "Custom Type", Kind: schema.NativeOther}, `CREATE TABLE "events" ("value" "Custom Type" NOT NULL)`},
		{"sqlite integer", dialect.SQLite(), &schema.NativeTypeDef{Dialect: "sqlite", Name: "INTEGER", Kind: schema.NativeOther}, `CREATE TABLE "events" ("value" INTEGER NOT NULL)`},
		{"sqlite int2", dialect.SQLite(), &schema.NativeTypeDef{Dialect: "sqlite", Name: "INT2", Kind: schema.NativeOther}, `CREATE TABLE "events" ("value" INT2 NOT NULL)`},
		{"sqlite int8", dialect.SQLite(), &schema.NativeTypeDef{Dialect: "sqlite", Name: "INT8", Kind: schema.NativeOther}, `CREATE TABLE "events" ("value" INT8 NOT NULL)`},
		{"sqlite double", dialect.SQLite(), &schema.NativeTypeDef{Dialect: "sqlite", Name: "DOUBLE PRECISION", Kind: schema.NativeOther}, `CREATE TABLE "events" ("value" "DOUBLE PRECISION" NOT NULL)`},
		{"sqlite boolean", dialect.SQLite(), &schema.NativeTypeDef{Dialect: "sqlite", Name: "BOOLEAN", Kind: schema.NativeOther}, `CREATE TABLE "events" ("value" BOOLEAN NOT NULL)`},
		{"sqlite json", dialect.SQLite(), &schema.NativeTypeDef{Dialect: "sqlite", Name: "JSON", Kind: schema.NativeOther}, `CREATE TABLE "events" ("value" JSON NOT NULL)`},
		{"sqlite date", dialect.SQLite(), &schema.NativeTypeDef{Dialect: "sqlite", Name: "DATE", Kind: schema.NativeOther}, `CREATE TABLE "events" ("value" DATE NOT NULL)`},
		{"sqlite time", dialect.SQLite(), &schema.NativeTypeDef{Dialect: "sqlite", Name: "TIME", Kind: schema.NativeOther}, `CREATE TABLE "events" ("value" TIME NOT NULL)`},
		{"sqlite blob", dialect.SQLite(), &schema.NativeTypeDef{Dialect: "sqlite", Name: "BLOB", Kind: schema.NativeOther}, `CREATE TABLE "events" ("value" BLOB NOT NULL)`},
		{"sqlite quoted declaration", dialect.SQLite(), &schema.NativeTypeDef{Dialect: "sqlite", Name: `A"B`, Kind: schema.NativeOther}, `CREATE TABLE "events" ("value" "A""B" NOT NULL)`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			statement, err := render.CreateTable(test.dialect, schema.TableDef{Name: "events", Columns: []schema.ColumnDef{{Name: "value", Type: schema.OpaqueType{}, NativeType: test.native}}})
			require.NoError(t, err)
			require.Equal(t, test.want, statement.SQL())
		})
	}
}

func TestCreateTableRejectsUnknownNativeShapesWithoutStatement(t *testing.T) {
	for _, native := range []*schema.NativeTypeDef{
		{Dialect: "postgresql", Name: "unknown", Kind: schema.NativeTypeKind("unknown")},
		{Dialect: "postgresql", Name: "broken", Kind: schema.NativeArray},
	} {
		statement, err := render.CreateTable(dialect.PostgreSQL(), schema.TableDef{Name: "events", Columns: []schema.ColumnDef{{Name: "value", Type: schema.OpaqueType{}, NativeType: native}}})
		require.Error(t, err)
		require.Empty(t, statement.SQL())
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

type dialectWithoutNativeNamer struct{ dialect.Dialect }

func TestCreateTableNativeRefusalMatrix(t *testing.T) {
	tests := []struct {
		name    string
		dialect dialect.Dialect
		native  *schema.NativeTypeDef
	}{
		{"postgres to mysql", dialect.MySQL(), &schema.NativeTypeDef{Dialect: "postgresql", Schema: "app", Name: "mood", Kind: schema.NativeEnum}},
		{"postgres to sqlite", dialect.SQLite(), &schema.NativeTypeDef{Dialect: "postgresql", Schema: "app", Name: "mood", Kind: schema.NativeEnum}},
		{"mysql to postgres", dialect.PostgreSQL(), &schema.NativeTypeDef{Dialect: "mysql", Name: "choice", Kind: schema.NativeSet, Arguments: []string{"a", "b"}}},
		{"mysql to sqlite", dialect.SQLite(), &schema.NativeTypeDef{Dialect: "mysql", Name: "choice", Kind: schema.NativeEnum, Arguments: []string{"a", "b"}}},
		{"sqlite to postgres", dialect.PostgreSQL(), &schema.NativeTypeDef{Dialect: "sqlite", Name: "VARCHAR", Kind: schema.NativeOther, Arguments: []string{"12"}}},
		{"sqlite to mysql", dialect.MySQL(), &schema.NativeTypeDef{Dialect: "sqlite", Name: "VARCHAR", Kind: schema.NativeOther, Arguments: []string{"12"}}},
		{"missing namer", dialectWithoutNativeNamer{Dialect: dialect.SQLite()}, &schema.NativeTypeDef{Dialect: "sqlite", Name: "VARCHAR", Kind: schema.NativeOther, Arguments: []string{"12"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := render.CreateTable(test.dialect, schema.TableDef{Name: "events", Columns: []schema.ColumnDef{{Name: "value", Type: schema.OpaqueType{}, NativeType: test.native}}})
			var unsupported *render.ErrUnsupportedNativeType
			require.ErrorAs(t, err, &unsupported)
			require.Equal(t, test.dialect.Name(), unsupported.Dialect)
			require.Equal(t, "events", unsupported.Table)
			require.Equal(t, "value", unsupported.Column)
			require.Equal(t, *test.native, unsupported.Native)
		})
	}
}
