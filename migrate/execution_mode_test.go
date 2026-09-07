package migrate

import (
	"testing"

	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

func TestExecutionModeValidationAndChecksumCompatibility(t *testing.T) {
	migration := Migration{ID: "001_mode", Statements: []Statement{{Source: "001.up.sql", SQL: sqltext.Text("SELECT 1")}}}
	require.NoError(t, migration.Validate())
	require.Equal(t, checksum(migration.Statements), checksumMode(ExecutionModeAtomic, migration.Statements))
	require.NotEqual(t, checksum(migration.Statements), checksumMode(ExecutionModeNonTransactional, migration.Statements))
	migration.Mode = ExecutionMode("invalid")
	require.ErrorContains(t, migration.Validate(), "invalid execution mode")
}

func TestPreparedMigrationExportsExecutionMode(t *testing.T) {
	prepared := preparedMigration{
		id: "001_mode", mode: ExecutionModeNonTransactional,
		statements: []Statement{{Source: "001.up.sql", SQL: sqltext.Text("SELECT 1")}},
		down:       []Statement{{Source: "001.down.sql", SQL: sqltext.Text("SELECT 0")}},
	}
	exported := exportMigrations([]preparedMigration{prepared})
	require.Len(t, exported, 1)
	require.Equal(t, ExecutionModeNonTransactional, exported[0].Mode)
	exported[0].Mode = ExecutionModeAtomic
	require.Equal(t, ExecutionModeNonTransactional, prepared.mode)
}
