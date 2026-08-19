//go:build unix

package migrate_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/stretchr/testify/require"
)

// TestRevertAgainstLiveDatabases runs a real revert on each live engine,
// because every other revert test in this package uses SQLite and SQLite is
// the one engine whose behavior here is least likely to surprise. What a
// live run proves is the part fixtures cannot: that the lock each engine
// takes, the delete against its own history table, and the reverse DDL all
// work against the server rather than only against rasql's own rendering.
func TestRevertAgainstLiveDatabases(t *testing.T) {
	for _, test := range []struct {
		name    string
		open    func(*testing.T) *sql.DB
		dialect dialect.Dialect
	}{
		{name: "postgresql", open: dbtest.PostgreSQLDB, dialect: dialect.PostgreSQL()},
		{name: "mysql", open: dbtest.MySQLDB, dialect: dialect.MySQL()},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := test.open(t)
			runner, err := migrate.NewWithHistoryTable(database, test.dialect, "revert_history")
			require.NoError(t, err)

			migrations := []migrate.Migration{
				reversibleMigration("001_users", `CREATE TABLE revert_users (id INTEGER NOT NULL PRIMARY KEY)`, `DROP TABLE revert_users`),
				reversibleMigration("002_projects", `CREATE TABLE revert_projects (id INTEGER NOT NULL PRIMARY KEY)`, `DROP TABLE revert_projects`),
			}
			require.NoError(t, runner.Apply(t.Context(), migrations...))
			require.True(t, liveTableExists(t, database, test.dialect.Name(), "revert_projects"))

			reverted, err := runner.Revert(t.Context(), migrate.Steps(1), migrations...)
			require.NoError(t, err)
			require.Equal(t, []string{"002_projects"}, revertedIDs(reverted))
			require.False(t, liveTableExists(t, database, test.dialect.Name(), "revert_projects"),
				"the reverse SQL must have reached the server")
			require.True(t, liveTableExists(t, database, test.dialect.Name(), "revert_users"),
				"a revert must stop where it was told to")

			entries, err := runner.Status(t.Context(), migrations...)
			require.NoError(t, err)
			require.Contains(t, entries, migrate.StatusEntry{ID: "002_projects", State: migrate.StatusPending},
				"a reverted migration becomes pending again")

			require.NoError(t, runner.Apply(t.Context(), migrations...))
			require.True(t, liveTableExists(t, database, test.dialect.Name(), "revert_projects"))
		})
	}
}

// TestFailedRevertAgainstLiveDatabases pins the asymmetry Revert documents
// rather than trusting the doc comment: PostgreSQL rolls a failed revert
// back whole, and MySQL does not, because its DDL commits implicitly. The
// MySQL half is the reason the doc tells a user to resolve the state by
// hand, so it is asserted here rather than described.
func TestFailedRevertAgainstLiveDatabases(t *testing.T) {
	for _, test := range []struct {
		name string
		open func(*testing.T) *sql.DB
		// firstReverseSurvives is whether the reverse statement that
		// succeeded is still in effect after a later one fails.
		firstReverseSurvives bool
		dialect              dialect.Dialect
	}{
		{name: "postgresql", open: dbtest.PostgreSQLDB, dialect: dialect.PostgreSQL(), firstReverseSurvives: false},
		{name: "mysql", open: dbtest.MySQLDB, dialect: dialect.MySQL(), firstReverseSurvives: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := test.open(t)
			runner, err := migrate.NewWithHistoryTable(database, test.dialect, "failed_revert_history")
			require.NoError(t, err)

			migrations := []migrate.Migration{
				reversibleMigration("001_first", `CREATE TABLE failed_first (id INTEGER NOT NULL PRIMARY KEY)`, `DROP TABLE failed_first`),
				reversibleMigration("002_second", `CREATE TABLE failed_second (id INTEGER NOT NULL PRIMARY KEY)`, `DROP TABLE failed_second`),
			}
			require.NoError(t, runner.Apply(t.Context(), migrations...))

			// The newest migration reverts cleanly and the one under it
			// fails, so the run gets one statement in before it stops.
			migrations[0].Down = []migrate.Statement{{Source: "001_x.down.sql", SQL: `DROP TABLE failed_absent`}}
			_, err = runner.Revert(t.Context(), migrate.Steps(2), migrations...)
			require.Error(t, err)

			require.Equal(t, test.firstReverseSurvives, !liveTableExists(t, database, test.dialect.Name(), "failed_second"),
				"whether the completed reverse statement survives the failure is the engine's DDL transactionality")
			require.True(t, liveTableExists(t, database, test.dialect.Name(), "failed_first"),
				"the reverse statement that failed changed nothing on either engine")
		})
	}
}

// liveTableExists asks the server itself, through the standard catalog both
// engines expose, rather than through rasql's own inspection. Each engine
// names the current schema its own way and takes its own placeholder, so
// the query is chosen by dialect rather than tried and retried.
func liveTableExists(t *testing.T, database *sql.DB, dialectName string, name string) bool {
	t.Helper()
	query := "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = CURRENT_SCHEMA() AND table_name = $1"
	if dialectName == "mysql" {
		query = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?"
	}
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), query, name).Scan(&count))
	return count > 0
}
