package dialect_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestBuiltinsRenderIdentifiersAndPlaceholders(t *testing.T) {
	tests := map[string]struct {
		dialect     dialect.Dialect
		identifier  string
		placeholder string
		typeName    string
	}{
		"postgresql": {
			dialect:     dialect.PostgreSQL(),
			identifier:  "\"order\"",
			placeholder: "$2",
			typeName:    "BYTEA",
		},
		"mysql": {
			dialect:     dialect.MySQL(),
			identifier:  "`order`",
			placeholder: "?",
			typeName:    "BLOB",
		},
		"sqlite": {
			dialect:     dialect.SQLite(),
			identifier:  "\"order\"",
			placeholder: "?",
			typeName:    "BLOB",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			identifier, err := test.dialect.QuoteIdentifier("order")
			require.NoError(t, err)
			require.Equal(t, test.identifier, identifier)

			placeholder, err := test.dialect.Placeholder(2)
			require.NoError(t, err)
			require.Equal(t, test.placeholder, placeholder)

			typeName, err := test.dialect.TypeName(schema.ColumnDef{Type: schema.BytesType{}})
			require.NoError(t, err)
			require.Equal(t, test.typeName, typeName)
		})
	}
}

func TestBuiltinsRejectInvalidInput(t *testing.T) {
	for _, test := range []dialect.Dialect{dialect.PostgreSQL(), dialect.MySQL(), dialect.SQLite()} {
		_, err := test.QuoteIdentifier("not-valid")
		require.Error(t, err)

		// A dotted string must never be accepted as one identifier: this is
		// the test that pins the injection guarantee behind schema
		// qualification. It proves the only way a dot reaches rendered SQL
		// is through two separately quoted segments, never through
		// QuoteIdentifier splitting or otherwise interpreting one.
		_, err = test.QuoteIdentifier("audit.events")
		require.Error(t, err)

		_, err = test.Placeholder(0)
		require.Error(t, err)

		_, err = test.TypeName(schema.ColumnDef{})
		require.ErrorContains(t, err, "unsupported nil column type")
	}
}

func TestBuiltinsRenderDecimalTypeNames(t *testing.T) {
	tests := map[string]struct {
		dialect       dialect.Dialect
		typeName      string
		scaleTypeName string
	}{
		"postgresql": {
			dialect:       dialect.PostgreSQL(),
			typeName:      "NUMERIC(19,4)",
			scaleTypeName: "NUMERIC(10,0)",
		},
		"mysql": {
			dialect:       dialect.MySQL(),
			typeName:      "DECIMAL(19,4)",
			scaleTypeName: "DECIMAL(10,0)",
		},
		"sqlite": {
			dialect:       dialect.SQLite(),
			typeName:      "TEXT",
			scaleTypeName: "TEXT",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			typeName, err := test.dialect.TypeName(schema.ColumnDef{Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}})
			require.NoError(t, err)
			require.Equal(t, test.typeName, typeName)

			scaleTypeName, err := test.dialect.TypeName(schema.ColumnDef{Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(0)}})
			require.NoError(t, err)
			require.Equal(t, test.scaleTypeName, scaleTypeName)
		})
	}
}

// TestBuiltinsRejectDecimalWithoutScale checks that every dialect, SQLite
// included, refuses a decimal column that states no scale, rather than
// rendering one whose meaning the descriptor never gave.
func TestBuiltinsRejectDecimalWithoutScale(t *testing.T) {
	for _, d := range []dialect.Dialect{dialect.PostgreSQL(), dialect.MySQL(), dialect.SQLite()} {
		t.Run(d.Name(), func(t *testing.T) {
			_, err := d.TypeName(schema.ColumnDef{Name: "amount", Type: schema.DecimalType{Precision: 19}})
			require.ErrorContains(t, err, `decimal column "amount" states no scale`)
		})
	}
}

func TestBuiltinsRejectUnrepresentableDecimals(t *testing.T) {
	_, err := dialect.MySQL().TypeName(schema.ColumnDef{Type: schema.DecimalType{Precision: 100, Scale: schema.NewDecimalScale(4)}})
	require.ErrorContains(t, err, "decimal precision 100 exceeds the maximum of 65")

	_, err = dialect.MySQL().TypeName(schema.ColumnDef{Type: schema.DecimalType{Precision: 31, Scale: schema.NewDecimalScale(31)}})
	require.ErrorContains(t, err, "decimal scale 31 exceeds the maximum of 30")

	_, err = dialect.PostgreSQL().TypeName(schema.ColumnDef{Type: schema.DecimalType{Precision: 1001, Scale: schema.NewDecimalScale(4)}})
	require.ErrorContains(t, err, "decimal precision 1001 exceeds the maximum of 1000")

	// SQLite has no bound, so none of the above precision/scale values error.
	for _, column := range []schema.ColumnDef{
		{Type: schema.DecimalType{Precision: 100, Scale: schema.NewDecimalScale(4)}},
		{Type: schema.DecimalType{Precision: 31, Scale: schema.NewDecimalScale(31)}},
		{Type: schema.DecimalType{Precision: 1001, Scale: schema.NewDecimalScale(4)}},
	} {
		typeName, err := dialect.SQLite().TypeName(column)
		require.NoError(t, err)
		require.Equal(t, "TEXT", typeName)
	}
}

// TestBuiltinsRenderUnsignedIntegerTypeNames pins the one dialect that can
// express an unsigned integer column and the two that cannot. MySQL renders
// BIGINT UNSIGNED, which reaches 18446744073709551615. PostgreSQL has no
// unsigned integer type and SQLite stores a signed 64-bit value whatever a
// column is declared, so both report an error naming the column instead of
// rendering a signed BIGINT that would reject values the descriptor permits.
func TestBuiltinsRenderUnsignedIntegerTypeNames(t *testing.T) {
	column := schema.ColumnDef{Name: "id", Type: schema.IntegerType{Unsigned: true}}

	typeName, err := dialect.MySQL().TypeName(column)
	require.NoError(t, err)
	require.Equal(t, "BIGINT UNSIGNED", typeName)

	for _, d := range []dialect.Dialect{dialect.PostgreSQL(), dialect.SQLite()} {
		t.Run(d.Name(), func(t *testing.T) {
			_, err := d.TypeName(column)
			require.ErrorContains(t, err, `unsigned integer column "id" cannot be represented`)
			require.ErrorContains(t, err, "has no unsigned integer type")
		})
	}

	// The signed column keeps the type name it always had, on every dialect.
	signed := schema.ColumnDef{Name: "id", Type: schema.IntegerType{}}
	for _, test := range []struct {
		dialect  dialect.Dialect
		typeName string
	}{
		{dialect: dialect.PostgreSQL(), typeName: "BIGINT"},
		{dialect: dialect.MySQL(), typeName: "BIGINT"},
		{dialect: dialect.SQLite(), typeName: "INTEGER"},
	} {
		typeName, err := test.dialect.TypeName(signed)
		require.NoError(t, err)
		require.Equal(t, test.typeName, typeName)
	}
}

// TestBuiltinsRenderTextTypeNames pins how each dialect renders
// schema.TextType.Width. An unstated width always renders the dialect's
// plain unbounded text type. A stated width renders VARCHAR(width) on
// PostgreSQL and MySQL, which enforce it on different terms: PostgreSQL
// rejects an over-length insert whatever the server settings, truncating
// instead only when the excess is all spaces, where MySQL rejects it only
// under strict SQL mode. SQLite renders plain TEXT regardless; it assigns
// column type by affinity rather than by declared type, so a VARCHAR(n)
// column there would store and enforce exactly like TEXT, and rendering
// VARCHAR(n) syntax would claim an enforcement that never happens, the
// same reason it already drops DecimalType's precision and scale
// (TestBuiltinsRejectUnrepresentableDecimals).
func TestBuiltinsRenderTextTypeNames(t *testing.T) {
	tests := map[string]struct {
		dialect          dialect.Dialect
		unstatedTypeName string
		statedTypeName   string
	}{
		"postgresql": {
			dialect:          dialect.PostgreSQL(),
			unstatedTypeName: "TEXT",
			statedTypeName:   "VARCHAR(255)",
		},
		"mysql": {
			dialect:          dialect.MySQL(),
			unstatedTypeName: "TEXT",
			statedTypeName:   "VARCHAR(255)",
		},
		"sqlite": {
			dialect:          dialect.SQLite(),
			unstatedTypeName: "TEXT",
			statedTypeName:   "TEXT",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			unstated, err := test.dialect.TypeName(schema.ColumnDef{Type: schema.TextType{}})
			require.NoError(t, err)
			require.Equal(t, test.unstatedTypeName, unstated)

			stated, err := test.dialect.TypeName(schema.ColumnDef{Type: schema.TextType{Width: schema.NewTextWidth(255)}})
			require.NoError(t, err)
			require.Equal(t, test.statedTypeName, stated)
		})
	}
}

// TestBuiltinsRenderTextWidthZero pins that a stated width of 0 renders
// VARCHAR(0), not the dialect's plain unbounded text type: schema.TextWidth
// exists precisely so a stated 0 is distinguishable from no width at all.
func TestBuiltinsRenderTextWidthZero(t *testing.T) {
	for _, d := range []dialect.Dialect{dialect.PostgreSQL(), dialect.MySQL()} {
		t.Run(d.Name(), func(t *testing.T) {
			typeName, err := d.TypeName(schema.ColumnDef{Type: schema.TextType{Width: schema.NewTextWidth(0)}})
			require.NoError(t, err)
			require.Equal(t, "VARCHAR(0)", typeName)
		})
	}
}

// TestBuiltinsRenderFixedTextTypeNames pins how each dialect renders
// schema.TextType.Fixed: PostgreSQL and MySQL render CHAR(width) instead of
// VARCHAR(width) on a fixed-width column, while SQLite keeps rendering plain
// TEXT regardless, the same way it already ignores a stated width, since
// its type affinity never enforces either.
func TestBuiltinsRenderFixedTextTypeNames(t *testing.T) {
	tests := map[string]struct {
		dialect       dialect.Dialect
		fixedTypeName string
	}{
		"postgresql": {dialect: dialect.PostgreSQL(), fixedTypeName: "CHAR(36)"},
		"mysql":      {dialect: dialect.MySQL(), fixedTypeName: "CHAR(36)"},
		"sqlite":     {dialect: dialect.SQLite(), fixedTypeName: "TEXT"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fixed, err := test.dialect.TypeName(schema.ColumnDef{Type: schema.TextType{Width: schema.NewTextWidth(36), Fixed: true}})
			require.NoError(t, err)
			require.Equal(t, test.fixedTypeName, fixed)
		})
	}
}

func TestBuiltinsRejectNilColumnType(t *testing.T) {
	for _, d := range []dialect.Dialect{dialect.PostgreSQL(), dialect.MySQL(), dialect.SQLite()} {
		t.Run(d.Name(), func(t *testing.T) {
			_, err := d.TypeName(schema.ColumnDef{Name: "amount"})
			require.ErrorContains(t, err, "unsupported nil column type")
		})
	}
}

func TestBuiltinCapabilities(t *testing.T) {
	require.True(t, dialect.PostgreSQL().Supports(dialect.CapabilityReturning))
	require.True(t, dialect.SQLite().Supports(dialect.CapabilityConflictTarget))
	require.False(t, dialect.MySQL().Supports(dialect.CapabilityConflictTarget))
	require.True(t, dialect.PostgreSQL().Supports(dialect.CapabilityDefaultValues))
	require.True(t, dialect.PostgreSQL().Supports(dialect.CapabilityDefaultValuesUpsert))
	require.True(t, dialect.MySQL().Supports(dialect.CapabilityDefaultValuesUpsert))
	require.True(t, dialect.MySQL().Supports(dialect.CapabilityEmptyInsert))
	require.False(t, dialect.SQLite().Supports(dialect.CapabilityDefaultValuesUpsert))
	require.False(t, dialect.MySQL().Supports(dialect.CapabilityReturning))
	require.Equal(t, dialect.UpsertOnConflict, dialect.PostgreSQL().UpsertStyle())
	require.Equal(t, dialect.UpsertDuplicateKey, dialect.MySQL().UpsertStyle())

	require.True(t, dialect.PostgreSQL().Supports(dialect.CapabilityQualifiedReference))
	require.True(t, dialect.PostgreSQL().Supports(dialect.CapabilityQualifiedIndexTarget))
	require.False(t, dialect.PostgreSQL().Supports(dialect.CapabilityQualifiedIndexName))

	require.True(t, dialect.MySQL().Supports(dialect.CapabilityQualifiedReference))
	require.True(t, dialect.MySQL().Supports(dialect.CapabilityQualifiedIndexTarget))
	require.False(t, dialect.MySQL().Supports(dialect.CapabilityQualifiedIndexName))

	require.False(t, dialect.SQLite().Supports(dialect.CapabilityQualifiedReference))
	require.False(t, dialect.SQLite().Supports(dialect.CapabilityQualifiedIndexTarget))
	require.True(t, dialect.SQLite().Supports(dialect.CapabilityQualifiedIndexName))

	require.True(t, dialect.PostgreSQL().Supports(dialect.CapabilityPartialIndex))
	require.True(t, dialect.SQLite().Supports(dialect.CapabilityPartialIndex))
	require.False(t, dialect.MySQL().Supports(dialect.CapabilityPartialIndex))

	require.True(t, dialect.SQLite().Supports(dialect.CapabilityMatchOperator))
	require.False(t, dialect.PostgreSQL().Supports(dialect.CapabilityMatchOperator))
	require.False(t, dialect.MySQL().Supports(dialect.CapabilityMatchOperator))
}
