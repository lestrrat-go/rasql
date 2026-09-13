//go:build unix

package migrate_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

func TestMySQLAlreadyAppliedTolerance(t *testing.T) {
	t.Run("apply tolerates each number", testMySQLApplyToleratesEachNumber)
	t.Run("atomic apply tolerates after implicit commit", testMySQLAtomicApplyTolerates)
	t.Run("revert tolerates an absent column", testMySQLRevertToleratesAbsentColumn)
}

func testMySQLApplyToleratesEachNumber(t *testing.T) {
	cases := []struct {
		name    string
		prefix  string
		setup   []string
		first   string
		number  uint16
		message string
	}{
		{
			name:    "1050 repeated CREATE TABLE",
			prefix:  "tol_create",
			first:   "CREATE TABLE tol_create_obj (id BIGINT NOT NULL PRIMARY KEY) ENGINE=InnoDB",
			number:  1050,
			message: "Error 1050 (42S01): Table 'tol_create_obj' already exists",
		},
		{
			name:    "1060 repeated ADD COLUMN",
			prefix:  "tol_column",
			setup:   []string{"CREATE TABLE tol_column_obj (id BIGINT NOT NULL PRIMARY KEY) ENGINE=InnoDB"},
			first:   "ALTER TABLE tol_column_obj ADD COLUMN note VARCHAR(20)",
			number:  1060,
			message: "Error 1060 (42S21): Duplicate column name 'note'",
		},
		{
			name:    "1061 repeated CREATE INDEX",
			prefix:  "tol_index",
			setup:   []string{"CREATE TABLE tol_index_obj (id BIGINT NOT NULL PRIMARY KEY, note VARCHAR(20)) ENGINE=InnoDB"},
			first:   "CREATE INDEX tol_index_obj_note ON tol_index_obj (note)",
			number:  1061,
			message: "Error 1061 (42000): Duplicate key name 'tol_index_obj_note'",
		},
		{
			name:    "1091 DROP COLUMN that is already gone",
			prefix:  "tol_drop",
			setup:   []string{"CREATE TABLE tol_drop_obj (id BIGINT NOT NULL PRIMARY KEY, note VARCHAR(20)) ENGINE=InnoDB"},
			first:   "ALTER TABLE tol_drop_obj DROP COLUMN note",
			number:  1091,
			message: "Error 1091 (42000): Can't DROP 'note'; check that column/key exists",
		},
		{
			name:    "3821 repeated DROP CHECK",
			prefix:  "tol_check",
			setup:   []string{"CREATE TABLE tol_check_obj (id BIGINT NOT NULL PRIMARY KEY, amount INT, CONSTRAINT tol_check_amount CHECK (amount > 0)) ENGINE=InnoDB"},
			first:   "ALTER TABLE tol_check_obj DROP CHECK tol_check_amount",
			number:  3821,
			message: "Error 3821 (HY000): Check constraint 'tol_check_amount' is not found in the table.",
		},
		{
			name:    "3940 repeated DROP CONSTRAINT",
			prefix:  "tol_constraint",
			setup:   []string{"CREATE TABLE tol_constraint_obj (id BIGINT NOT NULL PRIMARY KEY, amount INT, CONSTRAINT tol_constraint_amount CHECK (amount > 0)) ENGINE=InnoDB"},
			first:   "ALTER TABLE tol_constraint_obj DROP CONSTRAINT tol_constraint_amount",
			number:  3940,
			message: "Error 3940 (HY000): Constraint 'tol_constraint_amount' does not exist.",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			database := dbtest.MySQLDB(t)
			ctx := t.Context()
			for _, statement := range testCase.setup {
				mysqlExec(t, ctx, database, statement)
			}
			notices := &bytes.Buffer{}
			runner, err := migrate.NewWithHistoryTable(database, dialect.MySQL(), testCase.prefix+"_history")
			require.NoError(t, err)
			runner = runner.WithNotices(notices)
			missing := testCase.prefix + "_missing"
			migration := migrate.Migration{ID: "001_" + testCase.prefix, Mode: migrate.ExecutionModeNonTransactional, Statements: []migrate.Statement{
				{Source: "001_change.up.sql", SQL: sqltext.Text(testCase.first)},
				{Source: "002_fail.up.sql", SQL: sqltext.Text("INSERT INTO " + missing + " VALUES (1)")},
			}}
			_, firstErr := runner.Apply(ctx, migrate.AllPending(), migration)
			requireMySQLNativeError(t, firstErr, 1146)
			require.Equal(t, 0, mysqlHistoryCount(t, ctx, database, testCase.prefix+"_history", migration.ID))
			require.Empty(t, notices.String(), "nothing was tolerated on the first attempt")

			mysqlExec(t, ctx, database, "CREATE TABLE "+missing+" (id BIGINT NOT NULL) ENGINE=InnoDB")
			completed, retryErr := runner.Apply(ctx, migrate.AllPending(), migration)
			require.NoError(t, retryErr, "the retry tolerates MySQL %d and finishes the migration", testCase.number)
			require.Len(t, completed, 1)
			require.Equal(t, 1, mysqlHistoryCount(t, ctx, database, testCase.prefix+"_history", migration.ID))
			status, statusErr := runner.Status(ctx, migration)
			require.NoError(t, statusErr)
			require.Equal(t, migrate.StatusApplied, status[0].State)
			require.Equal(t, fmt.Sprintf("migrate: warning: migration %q SQL source %q was already applied: %s\n", migration.ID, "001_change.up.sql", testCase.message), notices.String())
		})
	}
}

func testMySQLAtomicApplyTolerates(t *testing.T) {
	database := dbtest.MySQLDB(t)
	ctx := t.Context()
	notices := &bytes.Buffer{}
	runner, err := migrate.NewWithHistoryTable(database, dialect.MySQL(), "tol_atomic_history")
	require.NoError(t, err)
	runner = runner.WithNotices(notices)
	migration := migrate.Migration{ID: "001_tol_atomic", Statements: []migrate.Statement{
		{Source: "001_create.up.sql", SQL: sqltext.Text("CREATE TABLE tol_atomic_obj (id BIGINT NOT NULL PRIMARY KEY) ENGINE=InnoDB")},
		{Source: "002_fail.up.sql", SQL: sqltext.Text("INSERT INTO tol_atomic_missing VALUES (1)")},
	}}
	_, firstErr := runner.Apply(ctx, migrate.AllPending(), migration)
	requireMySQLNativeError(t, firstErr, 1146)
	require.True(t, mysqlTableExists(t, ctx, database, "tol_atomic_obj"), "MySQL commits DDL implicitly, so an atomic migration leaves the created table behind")
	require.Equal(t, 0, mysqlHistoryCount(t, ctx, database, "tol_atomic_history", migration.ID))

	mysqlExec(t, ctx, database, "CREATE TABLE tol_atomic_missing (id BIGINT NOT NULL) ENGINE=InnoDB")
	completed, retryErr := runner.Apply(ctx, migrate.AllPending(), migration)
	require.NoError(t, retryErr)
	require.Len(t, completed, 1)
	require.Equal(t, 1, mysqlHistoryCount(t, ctx, database, "tol_atomic_history", migration.ID))
	require.Equal(t, "migrate: warning: migration \"001_tol_atomic\" SQL source \"001_create.up.sql\" was already applied: Error 1050 (42S01): Table 'tol_atomic_obj' already exists\n", notices.String())
	var rows int
	require.NoError(t, database.QueryRowContext(ctx, "SELECT COUNT(*) FROM tol_atomic_missing").Scan(&rows))
	require.Equal(t, 1, rows, "the statement after the tolerated one ran and its transaction committed")
}

func testMySQLRevertToleratesAbsentColumn(t *testing.T) {
	database := dbtest.MySQLDB(t)
	ctx := t.Context()
	mysqlExec(t, ctx, database, "CREATE TABLE tol_revert_obj (id BIGINT NOT NULL PRIMARY KEY) ENGINE=InnoDB")
	notices := &bytes.Buffer{}
	runner, err := migrate.NewWithHistoryTable(database, dialect.MySQL(), "tol_revert_history")
	require.NoError(t, err)
	runner = runner.WithNotices(notices)
	migration := migrate.Migration{
		ID:         "001_tol_revert",
		Mode:       migrate.ExecutionModeNonTransactional,
		Statements: []migrate.Statement{{Source: "001_add.up.sql", SQL: sqltext.Text("ALTER TABLE tol_revert_obj ADD COLUMN note VARCHAR(20)")}},
		Down: []migrate.Statement{
			{Source: "001_add.down.sql", SQL: sqltext.Text("ALTER TABLE tol_revert_obj DROP COLUMN note")},
			{Source: "002_fail.down.sql", SQL: sqltext.Text("INSERT INTO tol_revert_missing VALUES (1)")},
		},
	}
	requireApplied(t, ctx, runner, migration)
	_, firstErr := runner.Revert(ctx, migrate.Steps(1), migration)
	requireMySQLNativeError(t, firstErr, 1146)
	require.Equal(t, 1, mysqlHistoryCount(t, ctx, database, "tol_revert_history", migration.ID))

	mysqlExec(t, ctx, database, "CREATE TABLE tol_revert_missing (id BIGINT NOT NULL) ENGINE=InnoDB")
	completed, retryErr := runner.Revert(ctx, migrate.Steps(1), migration)
	require.NoError(t, retryErr)
	require.Len(t, completed, 1)
	require.Equal(t, 0, mysqlHistoryCount(t, ctx, database, "tol_revert_history", migration.ID))
	require.Equal(t, "migrate: warning: migration \"001_tol_revert\" reverse SQL source \"001_add.down.sql\" was already reverted: Error 1091 (42000): Can't DROP 'note'; check that column/key exists\n", notices.String())
}
