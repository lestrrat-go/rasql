package rasqlmigrate

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/dbtest"
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
		{name: "leading comment", query: "-- operator check\nSELECT TRUE", want: migrate.ReconcileExecuted},
		{name: "CTE", query: "WITH result(value) AS (SELECT TRUE) SELECT value FROM result", want: migrate.ReconcileExecuted},
		{name: "semicolon literal", query: "SELECT 'contains;semicolon' = 'contains;semicolon'", want: migrate.ReconcileExecuted},
		{name: "no rows", query: "SELECT TRUE WHERE FALSE", wantError: "no rows"},
		{name: "multiple rows", query: "SELECT TRUE UNION ALL SELECT FALSE", wantError: "more than one"},
		{name: "null", query: "SELECT NULL", wantError: "NULL"},
		{name: "non boolean", query: "SELECT 'yes'", wantError: "couldn't convert"},
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

func writeRecoveryMigrationDirectory(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "migrations")
	directory := filepath.Join(root, "001_recovery")
	require.NoError(t, os.MkdirAll(directory, 0o755))
	for _, file := range []struct{ name, contents string }{
		{"001_create.up.sql", "CREATE TABLE recovery_one"},
		{"002_index.up.sql", "CREATE INDEX recovery_two"},
		{"003_seed.up.sql", "INSERT INTO recovery_three"},
		{"001_create.down.sql", "DROP TABLE recovery_one"},
		{"002_index.down.sql", "DROP INDEX recovery_two"},
		{"003_seed.down.sql", "DELETE FROM recovery_three"},
	} {
		require.NoError(t, os.WriteFile(filepath.Join(directory, file.name), []byte(file.contents), 0o644))
	}
	return root
}

func runRecoveryCommand(t *testing.T, fixture *dbtest.Recovery, args ...string) (string, error) {
	t.Helper()
	previous := openDatabase
	openDatabase = func(string, string) (*sql.DB, error) { return fixture.Open() }
	t.Cleanup(func() { openDatabase = previous })
	var output bytes.Buffer
	err := Run(args, &output, &output)
	return output.String(), err
}

func TestDiskRecoveryApplyStatusVerifyReconcileAndRetry(t *testing.T) {
	directory := writeRecoveryMigrationDirectory(t)
	for _, decision := range []struct {
		name, query string
	}{
		{name: "executed", query: "SELECT TRUE"},
		{name: "not executed", query: "SELECT FALSE"},
	} {
		t.Run(decision.name, func(t *testing.T) {
			fixture := dbtest.NewRecovery()
			fixture.FailMigrationAt(1)
			_, err := runRecoveryCommand(t, fixture, "apply", "-dir", directory, "-dialect", "mysql", "-dsn", fixture.DSN())
			require.Error(t, err)
			status, err := runRecoveryCommand(t, fixture, "status", "-dir", directory, "-dialect", "mysql", "-dsn", fixture.DSN())
			require.NoError(t, err)
			require.Contains(t, status, "incomplete\t001_recovery")
			require.Contains(t, status, "source=002_index.up.sql direction=up index=1")
			_, err = runRecoveryCommand(t, fixture, "verify", "-dir", directory, "-dialect", "mysql", "-dsn", fixture.DSN())
			require.ErrorContains(t, err, "002_index.up.sql")
			output, err := runRecoveryCommand(t, fixture, "reconcile", "-dir", directory, "-dialect", "mysql", "-dsn", fixture.DSN(), "-id", "001_recovery", "-check", decision.query)
			require.NoError(t, err)
			require.Equal(t, "reconciled\t001_recovery\t"+map[string]string{"SELECT TRUE": "executed", "SELECT FALSE": "not_executed"}[decision.query]+"\tincomplete\n", output)
			output, err = runRecoveryCommand(t, fixture, "apply", "-dir", directory, "-dialect", "mysql", "-dsn", fixture.DSN())
			require.NoError(t, err)
			require.Contains(t, output, "migration apply completed: 1 applied")
			snapshot := fixture.Snapshot()
			require.Nil(t, snapshot.Progress)
			require.Equal(t, 1, executionCount(snapshot, "CREATE TABLE recovery_one"))
			wantMiddle := 1
			if decision.name == "executed" {
				wantMiddle = 0
			}
			require.Equal(t, wantMiddle, executionCount(snapshot, "CREATE INDEX recovery_two"))
			require.Equal(t, 1, executionCount(snapshot, "INSERT INTO recovery_three"))
		})
	}
}

func TestDiskRecoveryRevertStatusVerifyReconcileAndRetry(t *testing.T) {
	directory := writeRecoveryMigrationDirectory(t)
	for _, decision := range []struct {
		name, query string
	}{
		{name: "executed", query: "SELECT TRUE"},
		{name: "not executed", query: "SELECT FALSE"},
	} {
		t.Run(decision.name, func(t *testing.T) {
			fixture := dbtest.NewRecovery()
			_, err := runRecoveryCommand(t, fixture, "apply", "-dir", directory, "-dialect", "mysql", "-dsn", fixture.DSN())
			require.NoError(t, err)
			fixture.FailMigrationAt(1)
			_, err = runRecoveryCommand(t, fixture, "revert", "-dir", directory, "-dialect", "mysql", "-dsn", fixture.DSN(), "-steps", "1")
			require.Error(t, err)
			status, err := runRecoveryCommand(t, fixture, "status", "-dir", directory, "-dialect", "mysql", "-dsn", fixture.DSN())
			require.NoError(t, err)
			require.Contains(t, status, "incomplete\t001_recovery")
			require.Contains(t, status, "source=002_index.down.sql direction=down index=1")
			_, err = runRecoveryCommand(t, fixture, "verify", "-dir", directory, "-dialect", "mysql", "-dsn", fixture.DSN())
			require.ErrorContains(t, err, "002_index.down.sql")
			output, err := runRecoveryCommand(t, fixture, "reconcile", "-dir", directory, "-dialect", "mysql", "-dsn", fixture.DSN(), "-id", "001_recovery", "-check", decision.query)
			require.NoError(t, err)
			require.Equal(t, "reconciled\t001_recovery\t"+map[string]string{"SELECT TRUE": "executed", "SELECT FALSE": "not_executed"}[decision.query]+"\tincomplete\n", output)
			output, err = runRecoveryCommand(t, fixture, "revert", "-dir", directory, "-dialect", "mysql", "-dsn", fixture.DSN(), "-steps", "1")
			require.NoError(t, err)
			require.Contains(t, output, "migration revert completed: 1 reverted")
			snapshot := fixture.Snapshot()
			require.Nil(t, snapshot.Progress)
			require.Empty(t, snapshot.History)
			require.Equal(t, 1, executionCount(snapshot, "DROP TABLE recovery_one"))
			wantMiddle := 1
			if decision.name == "executed" {
				wantMiddle = 0
			}
			require.Equal(t, wantMiddle, executionCount(snapshot, "DROP INDEX recovery_two"))
			require.Equal(t, 1, executionCount(snapshot, "DELETE FROM recovery_three"))
		})
	}
}

func TestDiskRecoveryReconcileArgumentAndCheckErrors(t *testing.T) {
	directory := writeRecoveryMigrationDirectory(t)
	fixture := dbtest.NewRecovery()
	fixture.FailMigrationAt(1)
	_, err := runRecoveryCommand(t, fixture, "apply", "-dir", directory, "-dialect", "mysql", "-dsn", fixture.DSN())
	require.Error(t, err)
	for _, test := range []struct{ name, query, want string }{
		{"id mismatch", "SELECT TRUE", "does not match"},
		{"no rows", "SELECT NO_ROWS", "no rows"},
		{"multiple rows", "SELECT MULTIPLE_ROWS", "more than one"},
		{"null", "SELECT NULL_CHECK", "NULL"},
		{"non boolean", "SELECT TEXT_CHECK", "couldn't convert"},
		{"query error", "SELECT ERROR_CHECK", "query failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := fixture.Snapshot()
			id := "001_recovery"
			if test.name == "id mismatch" {
				id = "wrong"
			}
			_, err := runRecoveryCommand(t, fixture, "reconcile", "-dir", directory, "-dialect", "mysql", "-dsn", fixture.DSN(), "-id", id, "-check", test.query)
			require.ErrorContains(t, err, test.want)
			after := fixture.Snapshot()
			require.Equal(t, before.History, after.History)
			require.Equal(t, before.Progress, after.Progress)
			require.Equal(t, before.Effects, after.Effects)
			require.Equal(t, before.Executions, after.Executions)
		})
	}
	before := fixture.Snapshot()
	_, err = runRecoveryCommand(t, fixture, "reconcile", "-dir", directory, "-dialect", "mysql", "-dsn", fixture.DSN(), "-id", "001_recovery", "-check", "SELECT TRUE", "extra")
	require.ErrorContains(t, err, "no positional arguments")
	after := fixture.Snapshot()
	require.Equal(t, before.History, after.History)
	require.Equal(t, before.Progress, after.Progress)
	require.Equal(t, before.Effects, after.Effects)
	require.Equal(t, before.Executions, after.Executions)
}

func executionCount(snapshot dbtest.Snapshot, prefix string) int {
	for query, count := range snapshot.Executions {
		if strings.HasPrefix(query, prefix) {
			return count
		}
	}
	return 0
}
