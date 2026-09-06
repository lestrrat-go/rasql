//go:build unix

package rasqlmigrate

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestMySQLCLIReconcileReadOnlyRejectsWritesAndExtraStatements(t *testing.T) {
	config := dbtest.MySQLConfig(t)
	database := dbtest.MySQLDB(t)
	effect := dbtest.UniqueName(t, "p7_cli_check_effect")
	migrationsRoot := filepath.Join(t.TempDir(), "migrations")
	migrationDir := filepath.Join(migrationsRoot, "001_cli_check")
	require.NoError(t, os.MkdirAll(migrationDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(migrationDir, "001_effect.up.sql"), []byte("CREATE TABLE "+effect+" (id INT PRIMARY KEY)"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(migrationDir, "002_failure.up.sql"), []byte("THIS IS INVALID SQL"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(migrationDir, "001_effect.down.sql"), []byte("DROP TABLE "+effect), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(migrationDir, "002_failure.down.sql"), []byte("DROP TABLE "+effect), 0o644))
	dsn := config.FormatDSN()
	var output bytes.Buffer
	require.Error(t, Run([]string{"apply", "-dir", migrationsRoot, "-dialect", "mysql", "-dsn", dsn}, &output, &output))
	history := "rasql_schema_migrations"
	var sourceIndex, nextIndex int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT source_index, next_index FROM "+history+"_progress").Scan(&sourceIndex, &nextIndex))
	require.Equal(t, 1, sourceIndex)
	require.Equal(t, 1, nextIndex)
	var rows int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+effect).Scan(&rows))
	require.Zero(t, rows)
	for _, query := range []string{
		"INSERT INTO " + effect + " (id) VALUES (1)",
		"SELECT TRUE; SELECT FALSE",
	} {
		output.Reset()
		err := Run([]string{"reconcile", "-dir", migrationsRoot, "-dialect", "mysql", "-dsn", dsn, "-id", "001_cli_check", "-check", query}, &output, &output)
		require.Error(t, err)
		var currentSource, currentNext int
		require.NoError(t, database.QueryRowContext(t.Context(), "SELECT source_index, next_index FROM "+history+"_progress").Scan(&currentSource, &currentNext))
		require.Equal(t, sourceIndex, currentSource)
		require.Equal(t, nextIndex, currentNext)
		require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+effect).Scan(&rows))
		require.Zero(t, rows)
	}
}
