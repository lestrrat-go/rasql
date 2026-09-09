package migrate_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// TestForwardOnlyMigrationDirectoryAppliesAndRefusesRevert proves the loader
// and the runner together, against a real embedded database rather than a
// Go-built Migration: a migration directory holding only .up.sql sources,
// with no .down.sql files and no .rasql-irreversible marker, must load,
// apply cleanly, and then refuse a revert that reaches it by naming it,
// rather than being refused at load time as it once was.
func TestForwardOnlyMigrationDirectoryAppliesAndRefusesRevert(t *testing.T) {
	root := t.TempDir()
	reversible := filepath.Join(root, "001_users")
	require.NoError(t, os.MkdirAll(reversible, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(reversible, "001_x.up.sql"), []byte(`CREATE TABLE "users" ("id" INTEGER PRIMARY KEY)`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(reversible, "001_x.down.sql"), []byte(`DROP TABLE "users"`), 0o600))
	forwardOnly := filepath.Join(root, "002_audit_log")
	require.NoError(t, os.MkdirAll(forwardOnly, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(forwardOnly, "001_x.up.sql"), []byte(`CREATE TABLE "audit_log" ("id" INTEGER PRIMARY KEY)`), 0o600))

	migrations, err := migrationdir.Load(root)
	require.NoError(t, err)
	require.Len(t, migrations, 2)
	require.Empty(t, migrations[1].Down, "a migration with no .down.sql sources loads with none, not an error")
	require.Empty(t, migrations[1].IrreversibleReason, "no marker means no stated reason")

	database, err := sql.Open("sqlite", filepath.Join(root, "application.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	database.SetMaxOpenConns(1)
	runner, err := migrate.New(database, dialect.SQLite())
	require.NoError(t, err)

	applied, err := runner.Apply(t.Context(), migrate.AllPending(), migrations...)
	require.NoError(t, err)
	require.Len(t, applied, 2, "forward operations work normally on a directory with no reverse sources")
	require.True(t, tableExists(t, database, "users"))
	require.True(t, tableExists(t, database, "audit_log"))

	_, err = runner.Revert(t.Context(), migrate.Steps(2), migrations...)
	require.ErrorContains(t, err, `migration "002_audit_log" has no reverse SQL source`)
	require.True(t, tableExists(t, database, "users"), "a refused revert changes nothing")
	require.True(t, tableExists(t, database, "audit_log"), "a refused revert changes nothing")

	entries, err := runner.Status(t.Context(), migrations...)
	require.NoError(t, err)
	require.Equal(t, migrate.StatusApplied, entries[0].State)
	require.True(t, entries[0].Reversible)
	require.Equal(t, migrate.StatusApplied, entries[1].State)
	require.False(t, entries[1].Reversible, "status reports the irreversible migration before a caller tries to revert it")
}
