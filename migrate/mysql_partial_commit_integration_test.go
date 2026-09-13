//go:build unix

package migrate_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

func TestMySQLPartialCommitVerification(t *testing.T) {
	t.Run("apply pure DML", testMySQLApplyPureDML)
	t.Run("revert pure DML", testMySQLRevertPureDML)
	t.Run("apply implicit commit DDL", testMySQLApplyImplicitCommitDDL)
	t.Run("apply non-idempotent DDL stays pending", testMySQLApplyNonIdempotentDDLStaysPending)
	t.Run("revert implicit commit DDL", testMySQLRevertImplicitCommitDDL)
}

func testMySQLApplyPureDML(t *testing.T) {
	database := dbtest.MySQLDB(t)
	ctx := t.Context()
	logMySQLDiagnostics(t, ctx, database)
	mysqlExec(t, ctx, database, "CREATE TABLE sec_t1_apply_dml_counter (value BIGINT NOT NULL) ENGINE=InnoDB")
	mysqlExec(t, ctx, database, "INSERT INTO sec_t1_apply_dml_counter VALUES (0)")
	require.Equal(t, "InnoDB", mysqlTableEngine(t, ctx, database, "sec_t1_apply_dml_counter"))
	runner, err := migrate.NewWithHistoryTable(database, dialect.MySQL(), "sec_t1_apply_dml_history")
	require.NoError(t, err)
	migration := migrate.Migration{ID: "001_apply_dml", Statements: []migrate.Statement{
		{Source: "001_increment.up.sql", SQL: sqltext.Text("UPDATE sec_t1_apply_dml_counter SET value = value + 1")},
		{Source: "002_fail.up.sql", SQL: sqltext.Text("INSERT INTO sec_t1_apply_dml_missing VALUES (1)")},
	}}
	_, firstErr := runner.Apply(ctx, migrate.AllPending(), migration)
	requireMySQLNativeError(t, firstErr, 1146)
	require.Equal(t, int64(0), mysqlCounter(t, ctx, database, "sec_t1_apply_dml_counter"))
	require.Equal(t, 0, mysqlHistoryCount(t, ctx, database, "sec_t1_apply_dml_history", migration.ID))
	retry := migration
	retry.Statements = append([]migrate.Statement(nil), migration.Statements[:1]...)
	retry.Statements = append(retry.Statements, migrate.Statement{Source: "002_fixed.up.sql", SQL: sqltext.Text("UPDATE sec_t1_apply_dml_counter SET value = value + 1")})
	completed, retryErr := runner.Apply(ctx, migrate.AllPending(), retry)
	require.NoError(t, retryErr)
	require.Len(t, completed, 1)
	require.Equal(t, int64(2), mysqlCounter(t, ctx, database, "sec_t1_apply_dml_counter"))
	require.Equal(t, 1, mysqlHistoryCount(t, ctx, database, "sec_t1_apply_dml_history", migration.ID))
}

func testMySQLRevertPureDML(t *testing.T) {
	database := dbtest.MySQLDB(t)
	ctx := t.Context()
	logMySQLDiagnostics(t, ctx, database)
	mysqlExec(t, ctx, database, "CREATE TABLE sec_t1_revert_dml_counter (value BIGINT NOT NULL) ENGINE=InnoDB")
	mysqlExec(t, ctx, database, "INSERT INTO sec_t1_revert_dml_counter VALUES (1)")
	require.Equal(t, "InnoDB", mysqlTableEngine(t, ctx, database, "sec_t1_revert_dml_counter"))
	runner, err := migrate.NewWithHistoryTable(database, dialect.MySQL(), "sec_t1_revert_dml_history")
	require.NoError(t, err)
	migration := migrate.Migration{
		ID:         "001_revert_dml",
		Statements: []migrate.Statement{{Source: "001_increment.up.sql", SQL: sqltext.Text("UPDATE sec_t1_revert_dml_counter SET value = value + 1")}},
		Down: []migrate.Statement{
			{Source: "001_decrement.down.sql", SQL: sqltext.Text("UPDATE sec_t1_revert_dml_counter SET value = value - 1")},
			{Source: "002_fail.down.sql", SQL: sqltext.Text("INSERT INTO sec_t1_revert_dml_missing VALUES (1)")},
		},
	}
	requireApplied(t, ctx, runner, migration)
	require.Equal(t, int64(2), mysqlCounter(t, ctx, database, "sec_t1_revert_dml_counter"))
	_, firstErr := runner.Revert(ctx, migrate.Steps(1), migration)
	requireMySQLNativeError(t, firstErr, 1146)
	require.Equal(t, int64(2), mysqlCounter(t, ctx, database, "sec_t1_revert_dml_counter"))
	require.Equal(t, 1, mysqlHistoryCount(t, ctx, database, "sec_t1_revert_dml_history", migration.ID))
	retry := migration
	retry.Down = append([]migrate.Statement(nil), migration.Down[:1]...)
	retry.Down = append(retry.Down, migrate.Statement{Source: "002_fixed.down.sql", SQL: sqltext.Text("UPDATE sec_t1_revert_dml_counter SET value = value - 1")})
	completed, retryErr := runner.Revert(ctx, migrate.Steps(1), retry)
	require.NoError(t, retryErr)
	require.Len(t, completed, 1)
	require.Equal(t, int64(0), mysqlCounter(t, ctx, database, "sec_t1_revert_dml_counter"))
	require.Equal(t, 0, mysqlHistoryCount(t, ctx, database, "sec_t1_revert_dml_history", migration.ID))
}

func testMySQLApplyImplicitCommitDDL(t *testing.T) {
	database := dbtest.MySQLDB(t)
	ctx := t.Context()
	logMySQLDiagnostics(t, ctx, database)
	runner, err := migrate.NewWithHistoryTable(database, dialect.MySQL(), "sec_t1_apply_ddl_history")
	require.NoError(t, err)
	migration := migrate.Migration{ID: "001_apply_ddl", Mode: migrate.ExecutionModeNonTransactional, Statements: []migrate.Statement{
		{Source: "001_create.up.sql", SQL: sqltext.Text("CREATE TABLE IF NOT EXISTS sec_t1_apply_ddl_object (id BIGINT NOT NULL PRIMARY KEY) ENGINE=InnoDB")},
		{Source: "002_fail.up.sql", SQL: sqltext.Text("INSERT INTO sec_t1_apply_ddl_missing VALUES (1)")},
	}}
	_, firstErr := runner.Apply(ctx, migrate.AllPending(), migration)
	requireMySQLNativeError(t, firstErr, 1146)
	require.True(t, mysqlTableExists(t, ctx, database, "sec_t1_apply_ddl_object"), "MySQL commits DDL implicitly, so the first source's table survives the second source's failure")
	require.Equal(t, "InnoDB", mysqlTableEngine(t, ctx, database, "sec_t1_apply_ddl_object"))
	require.Equal(t, 0, mysqlHistoryCount(t, ctx, database, "sec_t1_apply_ddl_history", migration.ID), "a migration is recorded only when every one of its sources succeeded")
	status, statusErr := runner.Status(ctx, migration)
	require.NoError(t, statusErr)
	require.Equal(t, migrate.StatusPending, status[0].State, "an unrecorded migration is pending; nothing else is recorded about the failed attempt")

	// Clear the obstacle and re-run. The first source is spelled
	// CREATE TABLE IF NOT EXISTS, so running it a second time is not an
	// error and the retry reaches the second source.
	mysqlExec(t, ctx, database, "CREATE TABLE sec_t1_apply_ddl_missing (id BIGINT NOT NULL) ENGINE=InnoDB")
	completed, retryErr := runner.Apply(ctx, migrate.AllPending(), migration)
	require.NoError(t, retryErr)
	require.Len(t, completed, 1)
	require.True(t, mysqlTableExists(t, ctx, database, "sec_t1_apply_ddl_object"))
	require.Equal(t, 1, mysqlHistoryCount(t, ctx, database, "sec_t1_apply_ddl_history", migration.ID))
}

// testMySQLApplyNonIdempotentDDLStaysPending is the other half of the lesson:
// the same failure with a plain CREATE TABLE leaves the migration pending and
// the retry fails on the first source with MySQL error 1050, because MySQL
// reports the table the earlier attempt created.
func testMySQLApplyNonIdempotentDDLStaysPending(t *testing.T) {
	database := dbtest.MySQLDB(t)
	ctx := t.Context()
	runner, err := migrate.NewWithHistoryTable(database, dialect.MySQL(), "sec_t1_apply_plain_history")
	require.NoError(t, err)
	migration := migrate.Migration{ID: "001_apply_plain", Mode: migrate.ExecutionModeNonTransactional, Statements: []migrate.Statement{
		{Source: "001_create.up.sql", SQL: sqltext.Text("CREATE TABLE sec_t1_apply_plain_object (id BIGINT NOT NULL PRIMARY KEY) ENGINE=InnoDB")},
		{Source: "002_fail.up.sql", SQL: sqltext.Text("INSERT INTO sec_t1_apply_plain_missing VALUES (1)")},
	}}
	_, firstErr := runner.Apply(ctx, migrate.AllPending(), migration)
	requireMySQLNativeError(t, firstErr, 1146)
	require.Equal(t, 0, mysqlHistoryCount(t, ctx, database, "sec_t1_apply_plain_history", migration.ID))
	mysqlExec(t, ctx, database, "CREATE TABLE sec_t1_apply_plain_missing (id BIGINT NOT NULL) ENGINE=InnoDB")
	_, retryErr := runner.Apply(ctx, migrate.AllPending(), migration)
	requireMySQLNativeError(t, retryErr, 1050)
	require.ErrorContains(t, retryErr, `migrate: execute migration "001_apply_plain" SQL source "001_create.up.sql"`)
	status, statusErr := runner.Status(ctx, migration)
	require.NoError(t, statusErr)
	require.Equal(t, migrate.StatusPending, status[0].State)
}

func testMySQLRevertImplicitCommitDDL(t *testing.T) {
	database := dbtest.MySQLDB(t)
	ctx := t.Context()
	logMySQLDiagnostics(t, ctx, database)
	runner, err := migrate.NewWithHistoryTable(database, dialect.MySQL(), "sec_t1_revert_ddl_history")
	require.NoError(t, err)
	migration := migrate.Migration{
		ID:         "001_revert_ddl",
		Mode:       migrate.ExecutionModeNonTransactional,
		Statements: []migrate.Statement{{Source: "001_create.up.sql", SQL: sqltext.Text("CREATE TABLE sec_t1_revert_ddl_object (id BIGINT NOT NULL PRIMARY KEY) ENGINE=InnoDB")}},
		Down: []migrate.Statement{
			{Source: "001_drop.down.sql", SQL: sqltext.Text("DROP TABLE IF EXISTS sec_t1_revert_ddl_object")},
			{Source: "002_fail.down.sql", SQL: sqltext.Text("INSERT INTO sec_t1_revert_ddl_missing VALUES (1)")},
		},
	}
	requireApplied(t, ctx, runner, migration)
	require.True(t, mysqlTableExists(t, ctx, database, "sec_t1_revert_ddl_object"))
	require.Equal(t, "InnoDB", mysqlTableEngine(t, ctx, database, "sec_t1_revert_ddl_object"))
	_, firstErr := runner.Revert(ctx, migrate.Steps(1), migration)
	requireMySQLNativeError(t, firstErr, 1146)
	require.False(t, mysqlTableExists(t, ctx, database, "sec_t1_revert_ddl_object"))
	require.Equal(t, 1, mysqlHistoryCount(t, ctx, database, "sec_t1_revert_ddl_history", migration.ID), "the history record is deleted only after every reverse source succeeded")
	status, statusErr := runner.Status(ctx, migration)
	require.NoError(t, statusErr)
	require.Equal(t, migrate.StatusApplied, status[0].State, "a migration whose revert failed is still recorded, so it is still applied")

	// Clear the obstacle and revert again. The first reverse source is
	// spelled DROP TABLE IF EXISTS, so running it on the table the earlier
	// attempt already dropped is not an error.
	mysqlExec(t, ctx, database, "CREATE TABLE sec_t1_revert_ddl_missing (id BIGINT NOT NULL) ENGINE=InnoDB")
	completed, retryErr := runner.Revert(ctx, migrate.Steps(1), migration)
	require.NoError(t, retryErr)
	require.Len(t, completed, 1)
	require.False(t, mysqlTableExists(t, ctx, database, "sec_t1_revert_ddl_object"))
	require.Equal(t, 0, mysqlHistoryCount(t, ctx, database, "sec_t1_revert_ddl_history", migration.ID))
}

func logMySQLDiagnostics(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	var autocommit int
	var version string
	require.NoError(t, database.QueryRowContext(ctx, "SELECT @@autocommit, @@version").Scan(&autocommit, &version))
	require.Equal(t, 1, autocommit)
	require.NotEmpty(t, version)
	var engine string
	require.NoError(t, database.QueryRowContext(ctx, "SELECT COALESCE(MAX(ENGINE), '') FROM information_schema.tables WHERE table_schema = DATABASE() AND ENGINE IS NOT NULL").Scan(&engine))
	t.Logf("mysql diagnostics: autocommit=%d version=%s existing_engine=%s", autocommit, version, engine)
}

func requireMySQLNativeError(t *testing.T, err error, expectedNumber uint16) {
	t.Helper()
	require.Error(t, err)
	var mysqlErr *gomysql.MySQLError
	require.True(t, errors.As(err, &mysqlErr), "expected native MySQL error, got %T: %v", err, err)
	require.Equal(t, expectedNumber, mysqlErr.Number)
	t.Logf("mysql native error: number=%d message=%s", mysqlErr.Number, mysqlErr.Message)
}

func mysqlExec(t *testing.T, ctx context.Context, database *sql.DB, statement string) {
	t.Helper()
	_, err := database.ExecContext(ctx, statement)
	require.NoError(t, err)
}

func mysqlCounter(t *testing.T, ctx context.Context, database *sql.DB, table string) int64 {
	t.Helper()
	var value int64
	require.NoError(t, database.QueryRowContext(ctx, "SELECT value FROM "+table).Scan(&value))
	return value
}

func mysqlHistoryCount(t *testing.T, ctx context.Context, database *sql.DB, table string, id string) int {
	t.Helper()
	var count int
	require.NoError(t, database.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE id = ?", id).Scan(&count))
	return count
}

func mysqlTableExists(t *testing.T, ctx context.Context, database *sql.DB, name string) bool {
	t.Helper()
	var count int
	require.NoError(t, database.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", name).Scan(&count))
	return count > 0
}

func mysqlTableEngine(t *testing.T, ctx context.Context, database *sql.DB, name string) string {
	t.Helper()
	var engine string
	require.NoError(t, database.QueryRowContext(ctx, "SELECT ENGINE FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", name).Scan(&engine))
	return engine
}
