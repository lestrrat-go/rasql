//go:build unix

package migrate_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

func TestMySQLLockReleasedAfterCancellation(t *testing.T) {
	database := dbtest.MySQLDB(t)
	runner, err := migrate.New(database, dialect.MySQL())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := runner.Apply(ctx, migrate.AllPending(), migrate.Migration{
			ID:         "001_cancellation_probe",
			Statements: []migrate.Statement{{Source: "001.sql", SQL: sqltext.Text("SELECT SLEEP(10)")}},
		})
		done <- err
	}()
	pollCtx, stopPolling := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopPolling()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		var owner sql.NullInt64
		require.NoError(t, database.QueryRowContext(pollCtx, "SELECT IS_USED_LOCK(?)", "rasql_schema_migrations").Scan(&owner))
		if owner.Valid {
			break
		}
		select {
		case <-pollCtx.Done():
			require.FailNow(t, "timed out waiting for migration lock ownership")
		case <-ticker.C:
		}
	}
	cancel()
	err = <-done
	require.Error(t, err)
	freeCtx, stopFreePoll := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopFreePoll()
	freeTicker := time.NewTicker(25 * time.Millisecond)
	defer freeTicker.Stop()
	for {
		var free int
		require.NoError(t, database.QueryRowContext(freeCtx, "SELECT IS_FREE_LOCK(?)", "rasql_schema_migrations").Scan(&free))
		if free == 1 {
			break
		}
		select {
		case <-freeCtx.Done():
			require.FailNow(t, "timed out waiting for migration lock release")
		case <-freeTicker.C:
		}
	}
	var usable int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT 1").Scan(&usable))
	require.Equal(t, 1, usable)
}
