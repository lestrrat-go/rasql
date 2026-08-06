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
			typeName, err := test.dialect.TypeName(schema.Column{Type: schema.TypeDecimal, Precision: 19, Scale: 4})
			require.NoError(t, err)
			require.Equal(t, test.typeName, typeName)

			scaleTypeName, err := test.dialect.TypeName(schema.Column{Type: schema.TypeDecimal, Precision: 10, Scale: 0})
			require.NoError(t, err)
			require.Equal(t, test.scaleTypeName, scaleTypeName)
		})
	}
}

func TestBuiltinsRejectUnrepresentableDecimals(t *testing.T) {
	_, err := dialect.MySQL().TypeName(schema.Column{Type: schema.TypeDecimal, Precision: 100, Scale: 4})
	require.ErrorContains(t, err, "decimal precision 100 exceeds the maximum of 65")

	_, err = dialect.MySQL().TypeName(schema.Column{Type: schema.TypeDecimal, Precision: 31, Scale: 31})
	require.ErrorContains(t, err, "decimal scale 31 exceeds the maximum of 30")

	_, err = dialect.PostgreSQL().TypeName(schema.Column{Type: schema.TypeDecimal, Precision: 1001, Scale: 4})
	require.ErrorContains(t, err, "decimal precision 1001 exceeds the maximum of 1000")

	// SQLite has no bound, so none of the above precision/scale values error.
	for _, column := range []schema.Column{
		{Type: schema.TypeDecimal, Precision: 100, Scale: 4},
		{Type: schema.TypeDecimal, Precision: 31, Scale: 31},
		{Type: schema.TypeDecimal, Precision: 1001, Scale: 4},
	} {
		typeName, err := dialect.SQLite().TypeName(column)
		require.NoError(t, err)
		require.Equal(t, "TEXT", typeName)
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
}
