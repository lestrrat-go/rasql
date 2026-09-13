package migrate_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// applyTestRunner opens a fresh in-memory database and returns a runner over
// it. An in-memory SQLite database belongs to one connection, so the pool is
// held to a single one.
func applyTestRunner(t *testing.T) migrate.Runner {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)
	runner, err := migrate.New(database, dialect.SQLite())
	require.NoError(t, err)
	return runner
}

func TestApplyReportsWhatItApplied(t *testing.T) {
	runner := applyTestRunner(t)
	first := sqlMigration("001_create_users", `CREATE TABLE "users" ("id" INTEGER PRIMARY KEY)`)
	second := sqlMigration("002_add_nickname", `ALTER TABLE "users" ADD COLUMN "nickname" TEXT`)

	applied, err := runner.Apply(t.Context(), migrate.AllPending(), first, second)
	require.NoError(t, err)
	require.Equal(t, []string{"001_create_users", "002_add_nickname"}, migrationIDs(applied))

	// A second run has nothing left to do and says so with an empty result
	// rather than an error.
	applied, err = runner.Apply(t.Context(), migrate.AllPending(), first, second)
	require.NoError(t, err)
	require.Empty(t, applied)
}

// TestApplyThroughStopsAtTheNamedMigration pins the mirror of Through: the
// named migration is applied and everything after it stays pending.
func TestApplyThroughStopsAtTheNamedMigration(t *testing.T) {
	runner := applyTestRunner(t)
	first := sqlMigration("001_create_users", `CREATE TABLE "users" ("id" INTEGER PRIMARY KEY)`)
	second := sqlMigration("002_add_nickname", `ALTER TABLE "users" ADD COLUMN "nickname" TEXT`)

	applied, err := runner.Apply(t.Context(), migrate.ApplyThrough("001_create_users"), first, second)
	require.NoError(t, err)
	require.Equal(t, []string{"001_create_users"}, migrationIDs(applied))

	entries, err := runner.Status(t.Context(), first, second)
	require.NoError(t, err)
	require.Equal(t, migrate.StatusApplied, entries[0].State)
	require.Equal(t, migrate.StatusPending, entries[1].State)

	// Naming a migration that is already applied applies nothing.
	applied, err = runner.Apply(t.Context(), migrate.ApplyThrough("001_create_users"), first, second)
	require.NoError(t, err)
	require.Empty(t, applied)
}

func TestApplyThroughRejectsAMigrationThatWasNotSupplied(t *testing.T) {
	runner := applyTestRunner(t)
	first := sqlMigration("001_create_users", `CREATE TABLE "users" ("id" INTEGER PRIMARY KEY)`)

	_, err := runner.Apply(t.Context(), migrate.ApplyThrough("002_add_nickname"), first)
	require.ErrorContains(t, err, "was not supplied")
}

// TestApplyPlanReportsPendingMigrationsAndChangesNothing pins that the plan
// reads the history rather than the directory, so an applied migration is
// left out of it.
func TestApplyPlanReportsPendingMigrationsAndChangesNothing(t *testing.T) {
	runner := applyTestRunner(t)
	first := sqlMigration("001_create_users", `CREATE TABLE "users" ("id" INTEGER PRIMARY KEY)`)
	second := sqlMigration("002_add_nickname", `ALTER TABLE "users" ADD COLUMN "nickname" TEXT`)
	requireApplied(t, t.Context(), runner, first)

	plan, err := runner.ApplyPlan(t.Context(), migrate.AllPending(), first, second)
	require.NoError(t, err)
	require.Equal(t, []string{"002_add_nickname"}, migrationIDs(plan))

	entries, err := runner.Status(t.Context(), first, second)
	require.NoError(t, err)
	require.Equal(t, migrate.StatusPending, entries[1].State)
}

// recordingConnector refuses every connection and counts the attempts, so a
// test can prove a Runner call reached the pool and still returned the zero
// ExecutionResult rather than a partly filled one.
type recordingConnector struct{ calls *int }

func (c recordingConnector) Connect(context.Context) (driver.Conn, error) {
	*c.calls++
	return nil, driver.ErrBadConn
}

func (c recordingConnector) Driver() driver.Driver { return recordingDriver{} }

type recordingDriver struct{}

func (recordingDriver) Open(string) (driver.Conn, error) { return nil, driver.ErrBadConn }

func TestApplyAndRevertResultReportNothingWhenTheConnectionFails(t *testing.T) {
	calls := 0
	database := sql.OpenDB(recordingConnector{calls: &calls})
	t.Cleanup(func() { _ = database.Close() })
	runner, err := migrate.New(database, dialect.SQLite())
	require.NoError(t, err)
	migration := migrate.Migration{
		ID:         "001",
		Statements: []migrate.Statement{{Source: "001.sql", SQL: sqltext.Text("SELECT 1")}},
		Down:       []migrate.Statement{{Source: "001.down.sql", SQL: sqltext.Text("SELECT 1")}},
	}
	applyResult, applyErr := runner.ApplyResult(t.Context(), migrate.AllPending(), migration)
	require.Equal(t, migrate.ExecutionResult{}, applyResult)
	require.EqualError(t, applyErr, "migrate: open database connection: driver: bad connection")
	revertResult, revertErr := runner.RevertResult(t.Context(), migrate.Steps(1), migration)
	require.Equal(t, migrate.ExecutionResult{}, revertResult)
	require.EqualError(t, revertErr, "migrate: open database connection: driver: bad connection")
	require.NotZero(t, calls)
}

func migrationIDs(migrations []migrate.Migration) []string {
	ids := make([]string, len(migrations))
	for index, migration := range migrations {
		ids[index] = migration.ID
	}
	return ids
}
