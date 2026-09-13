//go:build unix

package migrate

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

// TestPostgreSQLNonTransactionalFailureStaysPending pins rule 1 against a live
// PostgreSQL server: a nontransactional migration whose second source fails is
// not recorded, so it stays pending, while the first source's effect survives
// because a nontransactional source runs outside any transaction. It then pins
// rule 2's payoff: once the obstacle is cleared, the same migration re-runs
// from its first source and succeeds, because that source is spelled
// idempotently.
func TestPostgreSQLNonTransactionalFailureStaysPending(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	history := dbtest.UniqueName(t, "rofs_pg_history")
	table := dbtest.UniqueName(t, "rofs_pg_table")
	index := dbtest.UniqueName(t, "rofs_pg_index")
	missing := dbtest.UniqueName(t, "rofs_pg_absent")
	migration := Migration{ID: "001_pg_concurrent", Mode: ExecutionModeNonTransactional, Statements: []Statement{
		{Source: "001_table.up.sql", SQL: sqltext.Text("CREATE TABLE IF NOT EXISTS " + table + " (id BIGINT PRIMARY KEY)")},
		{Source: "002_index.up.sql", SQL: sqltext.Text("CREATE INDEX CONCURRENTLY IF NOT EXISTS " + index + " ON " + missing + " (id)")},
	}, Down: []Statement{
		{Source: "002_index.down.sql", SQL: sqltext.Text("DROP INDEX CONCURRENTLY IF EXISTS " + index)},
		{Source: "001_table.down.sql", SQL: sqltext.Text("DROP TABLE IF EXISTS " + table)},
	}}
	runner, err := NewWithHistoryTable(database, dialect.PostgreSQL(), history)
	require.NoError(t, err)

	_, err = runner.Apply(t.Context(), AllPending(), migration)
	require.ErrorContains(t, err, `migrate: execute migration "001_pg_concurrent" SQL source "002_index.up.sql"`)
	require.ErrorContains(t, err, missing)
	require.True(t, postgreSQLRelationExists(t, database, table), "the first source ran outside a transaction, so its table survives the later failure")
	status, err := runner.Status(t.Context(), migration)
	require.NoError(t, err)
	require.Equal(t, StatusPending, status[0].State, "an unrecorded migration is pending, with nothing else recorded about the attempt")

	// Clear the obstacle the second source tripped over and re-run. The first
	// source is spelled CREATE TABLE IF NOT EXISTS, so running it a second
	// time is not an error.
	_, err = database.ExecContext(t.Context(), "CREATE TABLE "+missing+" (id BIGINT PRIMARY KEY)")
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = database.Exec("DROP TABLE IF EXISTS " + missing) })
	completed, err := runner.Apply(t.Context(), AllPending(), migration)
	require.NoError(t, err)
	require.Len(t, completed, 1)
	status, err = runner.Status(t.Context(), migration)
	require.NoError(t, err)
	require.Equal(t, StatusApplied, status[0].State)

	_, err = runner.Revert(t.Context(), Steps(1), migration)
	require.NoError(t, err)
	require.False(t, postgreSQLRelationExists(t, database, index))
	require.False(t, postgreSQLRelationExists(t, database, table))
}

func TestPostgreSQLTwoRunnersAndStatusShareMigrationLock(t *testing.T) {
	config := dbtest.PostgreSQLConfig(t)
	database := dbtest.PostgreSQLDB(t)
	secondDatabase := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = secondDatabase.Close() })
	history := dbtest.UniqueName(t, "p8_pg_serial_history")
	table := dbtest.UniqueName(t, "p8_pg_serial_table")
	migration := Migration{ID: "001_serial", Mode: ExecutionModeNonTransactional, Statements: []Statement{{
		Source: "001_table.up.sql", SQL: sqltext.Text("CREATE TABLE " + table + " (id BIGINT PRIMARY KEY)"),
	}}, Down: []Statement{{Source: "001_table.down.sql", SQL: sqltext.Text("DROP TABLE " + table)}}}
	first, err := NewWithHistoryTable(database, dialect.PostgreSQL(), history)
	require.NoError(t, err)
	second, err := NewWithHistoryTable(secondDatabase, dialect.PostgreSQL(), history)
	require.NoError(t, err)
	entered := make(chan struct{})
	release := make(chan struct{})
	previousHook := historyWriteHook
	var held bool
	historyWriteHook = func(operation string) error {
		if operation == "history" && !held {
			held = true
			close(entered)
			select {
			case <-release:
			case <-time.After(10 * time.Second):
				return errPostgreSQLLiveHistoryFailure
			}
		}
		return nil
	}
	t.Cleanup(func() {
		historyWriteHook = previousHook
		select {
		case <-release:
		default:
			close(release)
		}
	})
	firstResult := make(chan error, 1)
	go func() {
		_, runErr := first.Apply(context.Background(), AllPending(), migration)
		firstResult <- runErr
	}()
	<-entered
	secondContext, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	_, secondErr := second.Apply(secondContext, AllPending(), migration)
	cancel()
	require.ErrorIs(t, secondErr, context.DeadlineExceeded)
	statusContext, statusCancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	_, statusErr := second.Status(statusContext, migration)
	statusCancel()
	require.ErrorIs(t, statusErr, context.DeadlineExceeded)
	close(release)
	require.NoError(t, <-firstResult)
	status, err := second.Status(t.Context(), migration)
	require.NoError(t, err)
	require.Equal(t, StatusApplied, status[0].State)
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+history).Scan(&count))
	require.Equal(t, 1, count)
}

var errPostgreSQLLiveHistoryFailure = errors.New("postgresql live history failure")

// postgreSQLRelationExists asks the server's own catalog, which covers a table
// and an index alike, rather than going through rasql's inspection.
func postgreSQLRelationExists(t *testing.T, database *sql.DB, name string) bool {
	t.Helper()
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM pg_class WHERE relname = $1", name).Scan(&count))
	return count > 0
}
