package migrate

import (
	"testing"

	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

// TestMigrationChecksumMatchesModeAndStatements pins Migration.Checksum to checksumMode, the
// unexported function migrate has always used to compute what it records in the history table.
// rasql.sum needs an exported way to get this same value; the two must never drift apart.
func TestMigrationChecksumMatchesModeAndStatements(t *testing.T) {
	statements := []Statement{{Source: "001.up.sql", SQL: sqltext.Text("CREATE TABLE t (id INTEGER)")}}
	down := []Statement{{Source: "001.down.sql", SQL: sqltext.Text("DROP TABLE t")}}

	atomic := Migration{ID: "001_t", Mode: ExecutionModeAtomic, Statements: statements, Down: down}
	got, err := atomic.Checksum()
	require.NoError(t, err)
	require.Equal(t, checksumMode(ExecutionModeAtomic, statements), got)

	nonTransactional := Migration{ID: "001_t", Mode: ExecutionModeNonTransactional, Statements: statements, Down: down}
	got, err = nonTransactional.Checksum()
	require.NoError(t, err)
	require.Equal(t, checksumMode(ExecutionModeNonTransactional, statements), got)
	atomicChecksum, err := atomic.Checksum()
	require.NoError(t, err)
	require.NotEqual(t, atomicChecksum, got)

	// Down is excluded: correcting the reverse source must not change the checksum.
	changedDown := atomic
	changedDown.Down = []Statement{{Source: "001.down.sql", SQL: sqltext.Text("DROP TABLE t CASCADE")}}
	changedDownChecksum, err := changedDown.Checksum()
	require.NoError(t, err)
	require.Equal(t, atomicChecksum, changedDownChecksum)

	// An invalid migration reports the same error Validate would.
	invalid := Migration{ID: "", Statements: statements}
	_, err = invalid.Checksum()
	require.Error(t, err)
	require.Equal(t, invalid.Validate(), err)
}
