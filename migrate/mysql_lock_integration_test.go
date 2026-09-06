//go:build unix

package migrate_test

import (
	"context"
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
			ID: "001_cancellation_probe",
			Statements: []migrate.Statement{{Source: "001.sql", SQL: sqltext.Text("SELECT SLEEP(10)")}},
		})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	err = <-done
	require.Error(t, err)
	var free int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT IS_FREE_LOCK(?)", "rasql_schema_migrations").Scan(&free))
	require.Equal(t, 1, free)
	var usable int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT 1").Scan(&usable))
	require.Equal(t, 1, usable)
}
