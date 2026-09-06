//go:build unix

package migrate_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

type liveProgressCheck struct {
	query    string
	decision migrate.ReconcileDecision
}

func (c *liveProgressCheck) Check(ctx context.Context, connection *sql.Conn, _ migrate.IncompleteMigration) (migrate.ReconcileDecision, error) {
	var executed bool
	if err := connection.QueryRowContext(ctx, c.query).Scan(&executed); err != nil {
		return "", err
	}
	if executed {
		c.decision = migrate.ReconcileExecuted
	} else {
		c.decision = migrate.ReconcileNotExecuted
	}
	return c.decision, nil
}

func TestMySQLProgressSurvivesRestartAndFinalizesWithoutReplay(t *testing.T) {
	config := dbtest.MySQLConfig(t)
	database := dbtest.MySQLDB(t)
	history := dbtest.UniqueName(t, "p7_history")
	table := dbtest.UniqueName(t, "p7_effect")
	migrations := []migrate.Migration{{
		ID: "001_progress",
		Statements: []migrate.Statement{
			{Source: "001_create.sql", SQL: sqltext.Text("CREATE TABLE " + table + " (id INT PRIMARY KEY)")},
			{Source: "002_fail.sql", SQL: sqltext.Text("CREATE TABLE " + table + " (id INT PRIMARY KEY)")},
		},
		Down: []migrate.Statement{
			{Source: "002_drop.sql", SQL: sqltext.Text("DROP TABLE " + table)},
			{Source: "001_drop.sql", SQL: sqltext.Text("DROP TABLE " + table)},
		},
	}}
	runner, err := migrate.NewWithHistoryTable(database, dialect.MySQL(), history)
	require.NoError(t, err)
	_, err = runner.Apply(t.Context(), migrate.AllPending(), migrations...)
	require.Error(t, err)
	var exists int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", table).Scan(&exists))
	require.Equal(t, 1, exists)
	require.NoError(t, database.Close())

	connector, err := mysql.NewConnector(config)
	require.NoError(t, err)
	restarted := sql.OpenDB(connector)
	t.Cleanup(func() { _ = restarted.Close() })
	restartedRunner, err := migrate.NewWithHistoryTable(restarted, dialect.MySQL(), history)
	require.NoError(t, err)
	entries, err := restartedRunner.Status(t.Context(), migrations...)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, migrate.StatusIncomplete, entries[0].State)
	require.NotNil(t, entries[0].Incomplete)
	check := &liveProgressCheck{query: "SELECT COUNT(*) = 1 FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = '" + table + "'"}
	require.NoError(t, restartedRunner.Reconcile(t.Context(), check, migrations...))
	require.Equal(t, migrate.ReconcileExecuted, check.decision)
	completed, err := restartedRunner.Apply(t.Context(), migrate.AllPending(), migrations...)
	require.NoError(t, err)
	require.Empty(t, completed)
	var historyCount int
	require.NoError(t, restarted.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+history).Scan(&historyCount))
	require.Equal(t, 1, historyCount)
}
