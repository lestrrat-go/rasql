//go:build unix

package rasqlmigrate

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestPostgreSQLCLIChangePlanCreateFlow and TestMySQLCLIChangePlanCreateFlow
// are the live counterparts of TestRunSQLiteChangePlanCreateFlow. The audit
// this producer answers named PostgreSQL and MySQL specifically as the
// engines catalog-drift checking has to hold up against, and
// TestRunSQLiteNonemptyChangePlanFlow already proved the changeplan/migrate
// side of the pipeline against a real engine, so what these two add is the
// same CLI producer, apply, and drift-check path proven against the two
// engines that pipeline was actually built for.
func TestPostgreSQLCLIChangePlanCreateFlow(t *testing.T) {
	config := dbtest.PostgreSQLConfig(t)
	database := dbtest.PostgreSQLDB(t)
	dsn := stdlib.RegisterConnConfig(config)
	t.Cleanup(func() { stdlib.UnregisterConnConfig(dsn) })
	runLiveChangePlanCreateFlow(t, database, "postgresql", "postgresql-17", dsn, dbtest.UniqueName(t, "d2cli_pg"))
}

func TestMySQLCLIChangePlanCreateFlow(t *testing.T) {
	database := dbtest.MySQLDB(t)
	dsn := dbtest.MySQLConfig(t).FormatDSN()
	runLiveChangePlanCreateFlow(t, database, "mysql", "mysql-8.4", dsn, dbtest.UniqueName(t, "d2cli_my"))
}

// runLiveChangePlanCreateFlow mirrors TestRunSQLiteChangePlanCreateFlow: a
// real "rasql schema update" writes -lock against dsn, a hand-written
// migration directory holds the pending table, "plan create" turns the two
// into a plan file by running that migration against dsn for real, and
// "plan check"/"apply" then drive the same plan file the way a reviewer
// would after plan create handed it to them.
func runLiveChangePlanCreateFlow(t *testing.T, database *sql.DB, dialectName, profileID, dsn, table string) {
	t.Helper()
	t.Cleanup(func() { _, _ = database.ExecContext(t.Context(), "DROP TABLE IF EXISTS "+table) })

	root := t.TempDir()
	configPath := filepath.Join(root, "rasql.json")
	configBytes, err := json.Marshal(map[string]any{
		"engine":  map[string]string{"dialect": dialectName, "profile": profileID},
		"schema":  map[string]string{"kind": "live", "identity": "cli-plan-create-" + dialectName},
		"package": "store",
		"output":  "internal/store",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configBytes, 0o600))
	var setupOutput, setupDiagnostics bytes.Buffer
	require.NoError(t, rasqlgen.RunTopLevelContext(t.Context(),
		[]string{"schema", "update", "-config", configPath, "-dsn", dsn}, &setupOutput, &setupDiagnostics),
		setupDiagnostics.String())
	lockPath := filepath.Join(root, "rasql.lock.json")

	migrationID := "0001_create_" + table
	migrationsDir := filepath.Join(root, "migrations", migrationID)
	require.NoError(t, os.MkdirAll(migrationsDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "0001.up.sql"),
		[]byte("CREATE TABLE "+table+" (id BIGINT PRIMARY KEY)\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "0001.down.sql"),
		[]byte("DROP TABLE "+table+"\n"), 0o600))

	planPath := filepath.Join(root, "plan.json")
	var output, diagnostics bytes.Buffer
	require.NoError(t, Run([]string{
		"plan", "create",
		"-lock", lockPath,
		"-dir", filepath.Join(root, "migrations"),
		"-dialect", dialectName,
		"-dsn", dsn,
		"-output", planPath,
	}, &output, &diagnostics), diagnostics.String())
	require.Equal(t, "created "+planPath+"\n", output.String())
	require.True(t, liveCLITableExists(t, database, dialectName, table))

	// plan create ran the migration for real against dsn to observe its
	// result; undo it by hand so dsn is back at -lock's baseline, the same
	// way TestRunSQLiteChangePlanCreateFlow drops the table plan create left
	// behind before treating the database as a fresh apply target.
	_, err = database.ExecContext(t.Context(), "DROP TABLE "+table)
	require.NoError(t, err)
	require.False(t, liveCLITableExists(t, database, dialectName, table))

	output.Reset()
	require.NoError(t, Run([]string{"plan", "check", "-file", planPath, "-dialect", dialectName, "-dsn", dsn},
		&output, &diagnostics), diagnostics.String())
	require.Contains(t, output.String(), "next-operation\t0\n")
	require.Contains(t, output.String(), "complete\tfalse\n")

	output.Reset()
	require.NoError(t, Run([]string{"apply", "-plan", planPath, "-dialect", dialectName, "-dsn", dsn},
		&output, &diagnostics), diagnostics.String())
	require.Equal(t, "applied-operation\t0\t"+migrationID+"\nmigration plan apply completed: 1 applied\n", output.String())
	require.True(t, liveCLITableExists(t, database, dialectName, table))

	output.Reset()
	require.NoError(t, Run([]string{"plan", "check", "-file", planPath, "-dialect", dialectName, "-dsn", dsn},
		&output, &diagnostics), diagnostics.String())
	require.Contains(t, output.String(), "complete\ttrue\n")
}

func liveCLITableExists(t *testing.T, database *sql.DB, dialectName, table string) bool {
	t.Helper()
	query := "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1"
	if dialectName == "mysql" {
		query = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?"
	}
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), query, table).Scan(&count))
	return count == 1
}
