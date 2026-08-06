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

			typeName, err := test.dialect.TypeName(schema.Column{Type: schema.TypeBytes})
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

		_, err = test.TypeName(schema.Column{Type: "unknown"})
		require.ErrorContains(t, err, "unsupported logical type")
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
			typeName, err := test.dialect.TypeName(schema.Column{Type: schema.TypeDecimal, Precision: 19, Scale: schema.NewDecimalScale(4)})
			require.NoError(t, err)
			require.Equal(t, test.typeName, typeName)

			scaleTypeName, err := test.dialect.TypeName(schema.Column{Type: schema.TypeDecimal, Precision: 10, Scale: schema.NewDecimalScale(0)})
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
			_, err := d.TypeName(schema.Column{Name: "amount", Type: schema.TypeDecimal, Precision: 19})
			require.ErrorContains(t, err, `decimal column "amount" states no scale`)
		})
	}
}

func TestBuiltinsRejectUnrepresentableDecimals(t *testing.T) {
	_, err := dialect.MySQL().TypeName(schema.Column{Type: schema.TypeDecimal, Precision: 100, Scale: schema.NewDecimalScale(4)})
	require.ErrorContains(t, err, "decimal precision 100 exceeds the maximum of 65")

	_, err = dialect.MySQL().TypeName(schema.Column{Type: schema.TypeDecimal, Precision: 31, Scale: schema.NewDecimalScale(31)})
	require.ErrorContains(t, err, "decimal scale 31 exceeds the maximum of 30")

	_, err = dialect.PostgreSQL().TypeName(schema.Column{Type: schema.TypeDecimal, Precision: 1001, Scale: schema.NewDecimalScale(4)})
	require.ErrorContains(t, err, "decimal precision 1001 exceeds the maximum of 1000")

	// SQLite has no bound, so none of the above precision/scale values error.
	for _, column := range []schema.Column{
		{Type: schema.TypeDecimal, Precision: 100, Scale: schema.NewDecimalScale(4)},
		{Type: schema.TypeDecimal, Precision: 31, Scale: schema.NewDecimalScale(31)},
		{Type: schema.TypeDecimal, Precision: 1001, Scale: schema.NewDecimalScale(4)},
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
	column := schema.Column{Name: "id", Type: schema.TypeInteger, Unsigned: true}

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
	signed := schema.Column{Name: "id", Type: schema.TypeInteger}
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

// TestBuiltinsRejectUnsignedNonIntegerColumn covers the descriptor
// schema.Table.Validate already rejects, since a dialect renders columns it is
// handed directly as well as through a validated table.
func TestBuiltinsRejectUnsignedNonIntegerColumn(t *testing.T) {
	for _, d := range []dialect.Dialect{dialect.PostgreSQL(), dialect.MySQL(), dialect.SQLite()} {
		t.Run(d.Name(), func(t *testing.T) {
			_, err := d.TypeName(schema.Column{Name: "amount", Type: schema.TypeDecimal, Precision: 19, Scale: schema.NewDecimalScale(4), Unsigned: true})
			require.ErrorContains(t, err, `column "amount" cannot be represented`)
			require.ErrorContains(t, err, "unsigned applies only to an integer column")
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
}
