//go:build unix

package migrate_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// TestMigrationChecksumMatchesRecordedHistory is a live-engine claim per CLAUDE.md: it applies a
// migration through Runner.Apply against a real server, reads the checksum column back out of the
// history table with a raw query the runner itself did not build, and requires it to equal
// Migration.Checksum(). A SQLite twin runs in-process beside the two servers, but does not
// substitute for them.
func TestMigrationChecksumMatchesRecordedHistory(t *testing.T) {
	tests := []struct {
		name    string
		open    func(*testing.T) *sql.DB
		dialect dialect.Dialect
	}{
		{name: "postgresql", open: dbtest.PostgreSQLDB, dialect: dialect.PostgreSQL()},
		{name: "mysql", open: dbtest.MySQLDB, dialect: dialect.MySQL()},
		{name: "sqlite", open: sqliteChecksumHistoryDB, dialect: dialect.SQLite()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := test.open(t)
			history := dbtest.UniqueName(t, "checksum_h")
			runner, err := migrate.NewWithHistoryTable(database, test.dialect, history)
			require.NoError(t, err)

			migration := migrate.Migration{
				ID:         "001_checksum_history",
				Statements: []migrate.Statement{{Source: "001.up.sql", SQL: sqltext.Text("CREATE TABLE checksum_history_t (id INTEGER)")}},
			}
			applied, err := runner.Apply(t.Context(), migrate.AllPending(), migration)
			require.NoError(t, err)
			require.Equal(t, []migrate.Migration{migration}, applied)

			want, err := migration.Checksum()
			require.NoError(t, err)
			require.NotEmpty(t, want)

			got := recordedChecksum(t, database, test.dialect, history, migration.ID)
			require.Equal(t, want, got, "the checksum migrate records in the history table must equal Migration.Checksum()")
		})
	}
}

// sqliteChecksumHistoryDB opens a fresh in-memory SQLite database, matching the pattern the other
// migrate tests use for a SQLite twin of a live-engine assertion.
func sqliteChecksumHistoryDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	database.SetMaxOpenConns(1)
	return database
}

// recordedChecksum reads the checksum column straight out of the history table with a query this
// test builds itself, rather than through any migrate API, so the assertion is against what the
// server actually holds.
func recordedChecksum(t *testing.T, database *sql.DB, d dialect.Dialect, history, id string) string {
	t.Helper()
	historySQL, err := d.QuoteIdentifier(history)
	require.NoError(t, err)
	idSQL, err := d.QuoteIdentifier("id")
	require.NoError(t, err)
	checksumSQL, err := d.QuoteIdentifier("checksum")
	require.NoError(t, err)
	placeholder, err := d.Placeholder(1)
	require.NoError(t, err)
	query := "SELECT " + checksumSQL + " FROM " + historySQL + " WHERE " + idSQL + " = " + placeholder
	var checksum string
	require.NoError(t, database.QueryRowContext(t.Context(), query, id).Scan(&checksum))
	return checksum
}
