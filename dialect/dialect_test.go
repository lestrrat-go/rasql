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

			typeName, err := test.dialect.TypeName(schema.TypeBytes)
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

		_, err = test.TypeName("unknown")
		require.Error(t, err)
	}
}

func TestBuiltinCapabilities(t *testing.T) {
	require.True(t, dialect.PostgreSQL().Supports(dialect.CapabilityReturning))
	require.True(t, dialect.SQLite().Supports(dialect.CapabilityConflictTarget))
	require.True(t, dialect.PostgreSQL().Supports(dialect.CapabilityDefaultValues))
	require.True(t, dialect.PostgreSQL().Supports(dialect.CapabilityDefaultValuesUpsert))
	require.True(t, dialect.MySQL().Supports(dialect.CapabilityDefaultValuesUpsert))
	require.True(t, dialect.MySQL().Supports(dialect.CapabilityEmptyInsert))
	require.False(t, dialect.SQLite().Supports(dialect.CapabilityDefaultValuesUpsert))
	require.False(t, dialect.MySQL().Supports(dialect.CapabilityReturning))
	require.Equal(t, dialect.UpsertOnConflict, dialect.PostgreSQL().UpsertStyle())
	require.Equal(t, dialect.UpsertDuplicateKey, dialect.MySQL().UpsertStyle())
}
