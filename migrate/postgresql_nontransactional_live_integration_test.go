//go:build unix

package migrate

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

func TestPostgreSQLNonTransactionalCheckpointRecovery(t *testing.T) {
	config := dbtest.PostgreSQLConfig(t)
	database := dbtest.PostgreSQLDB(t)
	history := dbtest.UniqueName(t, "p8_pg_recovery_history")
	table := dbtest.UniqueName(t, "p8_pg_recovery_table")
	index := dbtest.UniqueName(t, "p8_pg_recovery_index")
	migration := Migration{ID: "001_pg_concurrent", Mode: ExecutionModeNonTransactional, Statements: []Statement{
		{Source: "001_table.up.sql", SQL: sqltext.Text("CREATE TABLE " + table + " (id BIGINT PRIMARY KEY)")},
		{Source: "002_index.up.sql", SQL: sqltext.Text("CREATE INDEX CONCURRENTLY " + index + " ON " + table + " (id)")},
	}, Down: []Statement{
		{Source: "002_index.down.sql", SQL: sqltext.Text("DROP INDEX CONCURRENTLY " + index)},
		{Source: "001_table.down.sql", SQL: sqltext.Text("DROP TABLE " + table)},
	}}
	runner, err := NewWithHistoryTable(database, dialect.PostgreSQL(), history)
	require.NoError(t, err)
	previousHook := journalWriteHook
	checkpointCount := 0
	journalWriteHook = func(operation string) error {
		if operation == "checkpoint" {
			checkpointCount++
			if checkpointCount == 2 {
				return errPostgreSQLLiveJournalFailure
			}
		}
		return nil
	}
	t.Cleanup(func() { journalWriteHook = previousHook })
	_, err = runner.Apply(t.Context(), AllPending(), migration)
	require.ErrorIs(t, err, errPostgreSQLLiveJournalFailure)
	var sourceIndex, nextIndex int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT source_index, next_index FROM "+history+"_progress").Scan(&sourceIndex, &nextIndex))
	require.Equal(t, 1, sourceIndex)
	require.Equal(t, 1, nextIndex)
	require.NoError(t, database.Close())
	restartedDatabase := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = restartedDatabase.Close() })
	restarted, err := NewWithHistoryTable(restartedDatabase, dialect.PostgreSQL(), history)
	require.NoError(t, err)
	_, err = restarted.Apply(t.Context(), AllPending(), migration)
	var incomplete *IncompleteMigrationError
	require.ErrorAs(t, err, &incomplete)
	require.Contains(t, err.Error(), "reconcile")
	check := &postgresqlIndexCheck{index: index}
	require.NoError(t, restarted.Reconcile(t.Context(), check, migration))
	require.Equal(t, ReconcileExecuted, check.decision)
	completed, err := restarted.Apply(t.Context(), AllPending(), migration)
	require.NoError(t, err)
	require.Empty(t, completed)
	status, err := restarted.Status(t.Context(), migration)
	require.NoError(t, err)
	require.Equal(t, StatusApplied, status[0].State)
	_, err = restarted.Revert(t.Context(), Steps(1), migration)
	require.NoError(t, err)
	var remaining int
	require.NoError(t, restartedDatabase.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM pg_class WHERE relname = $1", index).Scan(&remaining))
	require.Zero(t, remaining)
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
	previousHook := journalWriteHook
	var checkpoint bool
	journalWriteHook = func(operation string) error {
		if operation == "checkpoint" && !checkpoint {
			checkpoint = true
			close(entered)
			select {
			case <-release:
			case <-time.After(10 * time.Second):
				return errPostgreSQLLiveJournalFailure
			}
		}
		return nil
	}
	t.Cleanup(func() {
		journalWriteHook = previousHook
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

var errPostgreSQLLiveJournalFailure = errorString("postgresql live journal failure")

type postgresqlIndexCheck struct {
	index    string
	decision ReconcileDecision
}

func (c *postgresqlIndexCheck) Check(ctx context.Context, connection *sql.Conn, _ IncompleteMigration) (ReconcileDecision, error) {
	var valid, ready bool
	if err := connection.QueryRowContext(ctx, "SELECT indisvalid, indisready FROM pg_index WHERE indexrelid = $1::regclass", c.index).Scan(&valid, &ready); err == sql.ErrNoRows {
		c.decision = ReconcileNotExecuted
	} else if err != nil {
		return "", err
	} else if valid && ready {
		c.decision = ReconcileExecuted
	} else {
		c.decision = ReconcileNotExecuted
	}
	return c.decision, nil
}
