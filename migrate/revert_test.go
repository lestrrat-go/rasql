package migrate_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// reversibleMigration builds a migration whose forward and reverse sources
// are both single statements, which is all these tests need to observe an
// order and a history.
func reversibleMigration(id string, up string, down string) migrate.Migration {
	return migrate.Migration{
		ID:         id,
		Statements: []migrate.Statement{{Source: "001_x.up.sql", SQL: sqltext.Text(up)}},
		Down:       []migrate.Statement{{Source: "001_x.down.sql", SQL: sqltext.Text(down)}},
	}
}

// revertFixture opens an in-memory SQLite database with three reversible
// migrations already applied, and returns the runner and the migrations.
func revertFixture(t *testing.T) (migrate.Runner, *sql.DB, []migrate.Migration) {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	database.SetMaxOpenConns(1)
	runner, err := migrate.New(database, dialect.SQLite())
	require.NoError(t, err)

	migrations := []migrate.Migration{
		reversibleMigration("001_users", `CREATE TABLE "users" ("id" INTEGER PRIMARY KEY)`, `DROP TABLE "users"`),
		reversibleMigration("002_projects", `CREATE TABLE "projects" ("id" INTEGER PRIMARY KEY)`, `DROP TABLE "projects"`),
		reversibleMigration("003_tasks", `CREATE TABLE "tasks" ("id" INTEGER PRIMARY KEY)`, `DROP TABLE "tasks"`),
	}
	requireApplied(t, t.Context(), runner, migrations...)
	return runner, database, migrations
}

func appliedIDs(t *testing.T, database *sql.DB) []string {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), "SELECT id FROM rasql_schema_migrations ORDER BY id")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

func tableExists(t *testing.T, database *sql.DB, name string) bool {
	t.Helper()
	var found string
	err := database.QueryRowContext(t.Context(), "SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&found)
	if err == sql.ErrNoRows {
		return false
	}
	require.NoError(t, err)
	return found == name
}

func TestRevertSteps(t *testing.T) {
	runner, database, migrations := revertFixture(t)

	reverted, err := runner.Revert(t.Context(), migrate.Steps(2), migrations...)
	require.NoError(t, err)
	require.Equal(t, []string{"003_tasks", "002_projects"}, revertedIDs(reverted),
		"a revert runs newest first")

	require.Equal(t, []string{"001_users"}, appliedIDs(t, database))
	require.True(t, tableExists(t, database, "users"))
	require.False(t, tableExists(t, database, "projects"))
	require.False(t, tableExists(t, database, "tasks"))
}

// TestRevertThroughLeavesTheNamedMigrationApplied pins the meaning of the
// -to flag: the database is left at the point where the named migration was
// applied, so that migration survives.
func TestRevertThroughLeavesTheNamedMigrationApplied(t *testing.T) {
	runner, database, migrations := revertFixture(t)

	reverted, err := runner.Revert(t.Context(), migrate.Through("001_users"), migrations...)
	require.NoError(t, err)
	require.Equal(t, []string{"003_tasks", "002_projects"}, revertedIDs(reverted))
	require.Equal(t, []string{"001_users"}, appliedIDs(t, database))
	require.True(t, tableExists(t, database, "users"))
}

func TestRevertThroughTheNewestMigrationRevertsNothing(t *testing.T) {
	runner, database, migrations := revertFixture(t)

	reverted, err := runner.Revert(t.Context(), migrate.Through("003_tasks"), migrations...)
	require.NoError(t, err)
	require.Empty(t, reverted)
	require.Equal(t, []string{"001_users", "002_projects", "003_tasks"}, appliedIDs(t, database))
}

func TestRevertPlanReportsWithoutChangingAnything(t *testing.T) {
	runner, database, migrations := revertFixture(t)

	plan, err := runner.RevertPlan(t.Context(), migrate.Steps(2), migrations...)
	require.NoError(t, err)
	require.Equal(t, []string{"003_tasks", "002_projects"}, revertedIDs(plan))
	require.Equal(t, sqltext.Text(`DROP TABLE "tasks"`), plan[0].Down[0].SQL)
	require.Equal(t, []string{"001_users", "002_projects", "003_tasks"}, appliedIDs(t, database),
		"a plan must not touch the history")
	require.True(t, tableExists(t, database, "tasks"))
}

func TestRevertRefusals(t *testing.T) {
	t.Run("no target", func(t *testing.T) {
		runner, database, migrations := revertFixture(t)
		_, err := runner.Revert(t.Context(), migrate.RevertTarget{}, migrations...)
		require.ErrorContains(t, err, "revert requires a target")
		require.Len(t, appliedIDs(t, database), 3)
	})

	t.Run("unapplied target", func(t *testing.T) {
		runner, _, migrations := revertFixture(t)
		_, err := runner.Revert(t.Context(), migrate.Through("004_absent"), migrations...)
		require.ErrorContains(t, err, `migration "004_absent" is not applied`)
	})

	t.Run("too many steps", func(t *testing.T) {
		runner, _, migrations := revertFixture(t)
		_, err := runner.Revert(t.Context(), migrate.Steps(4), migrations...)
		require.ErrorContains(t, err, "cannot revert 4 migrations; 3 are applied")
	})

	t.Run("non-positive steps", func(t *testing.T) {
		runner, _, migrations := revertFixture(t)
		_, err := runner.Revert(t.Context(), migrate.Steps(0), migrations...)
		require.ErrorContains(t, err, "revert step count 0 must be positive")
		_, err = runner.Revert(t.Context(), migrate.Steps(-1), migrations...)
		require.ErrorContains(t, err, "revert step count -1 must be positive")
	})

	// A migration with no reverse source cannot be reached from disk, since
	// the loader refuses it, but a Go caller can build one. The whole run is
	// refused rather than reverting down to it and stopping.
	t.Run("irreversible migration", func(t *testing.T) {
		runner, database, migrations := revertFixture(t)
		migrations[2].Down = nil
		_, err := runner.Revert(t.Context(), migrate.Steps(2), migrations...)
		require.ErrorContains(t, err, `migration "003_tasks" has no reverse SQL source`)
		require.Len(t, appliedIDs(t, database), 3, "a refused revert changes nothing")
		require.True(t, tableExists(t, database, "projects"))
	})

	t.Run("changed forward source", func(t *testing.T) {
		runner, database, migrations := revertFixture(t)
		migrations[2].Statements[0].SQL = `CREATE TABLE "tasks" ("id" INTEGER PRIMARY KEY, "title" TEXT)`
		_, err := runner.Revert(t.Context(), migrate.Steps(1), migrations...)
		require.ErrorContains(t, err, `migration "003_tasks" checksum does not match recorded migration`)
		require.Len(t, appliedIDs(t, database), 3)
	})

	t.Run("recorded migration not supplied", func(t *testing.T) {
		runner, _, migrations := revertFixture(t)
		_, err := runner.Revert(t.Context(), migrate.Steps(1), migrations[:2]...)
		require.ErrorContains(t, err, `recorded migration "003_tasks" was not supplied`)
	})
}

// TestRevertLeavesTheHistoryAccurateWhenAReverseSourceFails requires a
// failed revert to leave the database as it was, rather than deleting a
// history row for a migration whose reverse SQL did not run.
func TestRevertLeavesTheHistoryAccurateWhenAReverseSourceFails(t *testing.T) {
	runner, database, migrations := revertFixture(t)
	migrations[2].Down = []migrate.Statement{{Source: "001_x.down.sql", SQL: `DROP TABLE "absent"`}}

	_, err := runner.Revert(t.Context(), migrate.Steps(2), migrations...)
	require.ErrorContains(t, err, `execute migration "003_tasks" reverse SQL source`)
	require.Equal(t, []string{"001_users", "002_projects", "003_tasks"}, appliedIDs(t, database))
	require.True(t, tableExists(t, database, "projects"), "SQLite rolls the whole revert back")
}

// TestRevertAndApplyRoundTrip requires a reverted migration to become
// pending again, so the ordinary fix-and-reapply loop works.
func TestRevertAndApplyRoundTrip(t *testing.T) {
	runner, database, migrations := revertFixture(t)

	_, err := runner.Revert(t.Context(), migrate.Steps(1), migrations...)
	require.NoError(t, err)
	require.False(t, tableExists(t, database, "tasks"))

	entries, err := runner.Status(t.Context(), migrations...)
	require.NoError(t, err)
	require.Contains(t, entries, migrate.StatusEntry{ID: "003_tasks", State: migrate.StatusPending})

	requireApplied(t, t.Context(), runner, migrations...)
	require.True(t, tableExists(t, database, "tasks"))
	require.Equal(t, []string{"001_users", "002_projects", "003_tasks"}, appliedIDs(t, database))
}

func revertedIDs(migrations []migrate.Migration) []string {
	ids := make([]string, len(migrations))
	for index, migration := range migrations {
		ids[index] = migration.ID
	}
	return ids
}
