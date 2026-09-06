package rasqlmigrate

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/migrate"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestSQLReconcileCheckValidatesRowsAndCapturesDecision(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	connection, err := database.Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	for _, test := range []struct {
		name      string
		query     string
		want      migrate.ReconcileDecision
		wantError string
	}{
		{name: "executed", query: "SELECT TRUE", want: migrate.ReconcileExecuted},
		{name: "not executed", query: "SELECT FALSE", want: migrate.ReconcileNotExecuted},
		{name: "no rows", query: "SELECT TRUE WHERE FALSE", wantError: "no rows"},
		{name: "multiple rows", query: "SELECT TRUE UNION ALL SELECT FALSE", wantError: "more than one"},
		{name: "null", query: "SELECT NULL", wantError: "NULL"},
		{name: "non boolean", query: "SELECT 'yes'", wantError: "couldn't convert"},
		{name: "extra statement", query: "SELECT TRUE; SELECT FALSE", wantError: "exactly one"},
		{name: "write", query: "CREATE TABLE attempted_write (id INTEGER)", wantError: "read-only"},
	} {
		t.Run(test.name, func(t *testing.T) {
			check := &sqlReconcileCheck{id: "001", query: test.query}
			decision, err := check.Check(t.Context(), connection, migrate.IncompleteMigration{ID: "001"})
			if test.wantError != "" {
				require.ErrorContains(t, err, test.wantError)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, decision)
			require.Equal(t, test.want, check.observed)
		})
	}
}

func TestSQLReconcileCheckRejectsIDMismatch(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	connection, err := database.Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	check := &sqlReconcileCheck{id: "001", query: "SELECT TRUE"}
	_, err = check.Check(t.Context(), connection, migrate.IncompleteMigration{ID: "002"})
	require.ErrorContains(t, err, "does not match")
}

func TestDiskMigrationStatusAndVerifyUseRealDirectory(t *testing.T) {
	root := t.TempDir()
	migrationsRoot := filepath.Join(root, "migrations")
	migrationDir := filepath.Join(migrationsRoot, "001_users")
	require.NoError(t, os.MkdirAll(migrationDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(migrationDir, "001.up.sql"), []byte("CREATE TABLE users (id INTEGER PRIMARY KEY)"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(migrationDir, "001.down.sql"), []byte("DROP TABLE users"), 0o644))
	dsn := filepath.Join(root, "database.sqlite")
	var output bytes.Buffer
	require.NoError(t, Run([]string{"apply", "-dir", migrationsRoot, "-dialect", "sqlite", "-dsn", dsn}, &output, &output))
	output.Reset()
	require.NoError(t, Run([]string{"status", "-dir", migrationsRoot, "-dialect", "sqlite", "-dsn", dsn}, &output, &output))
	require.Contains(t, output.String(), "applied\t001_users")
	output.Reset()
	require.NoError(t, Run([]string{"verify", "-dir", migrationsRoot, "-dialect", "sqlite", "-dsn", dsn}, &output, &output))
	require.Contains(t, output.String(), "migration verification passed")
}
