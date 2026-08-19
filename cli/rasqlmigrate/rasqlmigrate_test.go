package rasqlmigrate

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/migrate/diff/mysql"
	"github.com/stretchr/testify/require"
)

// TestRunRejectsRemovedNewCommand pins the removal of the "new" subcommand,
// which created a migration directory and nothing inside it. A user makes
// that directory with mkdir; the layout it has to follow is stated in the
// usage block and in docs/10-migrations.md, and every rule that matters is
// enforced where the migrations are read and applied.
func TestRunRejectsRemovedNewCommand(t *testing.T) {
	setCommandOutput(t)
	err := run([]string{"new", "-dir", newTestDirectory(t), "-id", "002_add_nickname"})
	require.EqualError(t, err, `unknown rasqlmigrate command "new"`)
}

func TestRunDiffPreviewsAndWritesPostgreSQLMigration(t *testing.T) {
	baseline := filepath.Join(t.TempDir(), "baseline")
	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, baseline, "tables/members.sql", "CREATE TABLE members (id bigint PRIMARY KEY);\n")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id bigint PRIMARY KEY, email text);\n")
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{"diff", "-dialect", "postgresql", "-from", baseline, "-to", target}))
	require.Equal(t, "-- 001_add_column_members_email.sql: add column members.email\nALTER TABLE members ADD COLUMN email text;\n", outputBuffer.String())

	migrationDirectory := filepath.Join(t.TempDir(), "002_add_member_email")
	outputBuffer.Reset()
	require.NoError(t, run([]string{"diff", "-dialect", "postgresql", "-from", baseline, "-to", target, "-output", migrationDirectory}))
	require.Equal(t, "created "+migrationDirectory+"\n", outputBuffer.String())
	contents, err := os.ReadFile(filepath.Join(migrationDirectory, "001_add_column_members_email.sql"))
	require.NoError(t, err)
	require.Equal(t, "ALTER TABLE members ADD COLUMN email text;\n", string(contents))
}

func TestRunDiffPreviewsMySQLMigration(t *testing.T) {
	baseline := filepath.Join(t.TempDir(), "baseline")
	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, baseline, "tables/members.sql", "CREATE TABLE members (id bigint PRIMARY KEY);\n")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id bigint PRIMARY KEY, email text);\n")
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{"diff", "-dialect", "mysql", "-from", baseline, "-to", target}))
	require.Equal(t, "-- 001_add_column_members_email.sql: add column members.email\nALTER TABLE members ADD COLUMN email text;\n", outputBuffer.String())

	migrationDirectory := filepath.Join(t.TempDir(), "002_add_member_email")
	outputBuffer.Reset()
	require.NoError(t, run([]string{"diff", "-dialect", "mysql", "-from", baseline, "-to", target, "-output", migrationDirectory}))
	require.Equal(t, "created "+migrationDirectory+"\n", outputBuffer.String())
	contents, err := os.ReadFile(filepath.Join(migrationDirectory, "001_add_column_members_email.sql"))
	require.NoError(t, err)
	require.Equal(t, "ALTER TABLE members ADD COLUMN email text;\n", string(contents))
}

func TestRunDiffPreviewsSQLiteMigration(t *testing.T) {
	baseline := filepath.Join(t.TempDir(), "baseline")
	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, baseline, "tables/members.sql", "CREATE TABLE members (id integer PRIMARY KEY);\n")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id integer PRIMARY KEY, email text);\n")
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{"diff", "-dialect", "sqlite", "-from", baseline, "-to", target}))
	require.Equal(t, "-- 001_add_column_members_email.sql: add column members.email\nALTER TABLE members ADD COLUMN email text;\n", outputBuffer.String())

	migrationDirectory := filepath.Join(t.TempDir(), "002_add_member_email")
	outputBuffer.Reset()
	require.NoError(t, run([]string{"diff", "-dialect", "sqlite", "-from", baseline, "-to", target, "-output", migrationDirectory}))
	require.Equal(t, "created "+migrationDirectory+"\n", outputBuffer.String())
	contents, err := os.ReadFile(filepath.Join(migrationDirectory, "001_add_column_members_email.sql"))
	require.NoError(t, err)
	require.Equal(t, "ALTER TABLE members ADD COLUMN email text;\n", string(contents))
}

func TestRunDiffLivePreviewsSQLiteMigration(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "application.db")
	database, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`)
	require.NoError(t, err)
	require.NoError(t, database.Close())
	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT NOT NULL, email TEXT);\n")
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{
		"diff-live",
		"-dialect", "sqlite",
		"-dsn", dsn,
		"-table", "members",
		"-to", target,
	}))
	require.Equal(t, "-- 001_add_column_members_email.sql: add column members.email\nALTER TABLE members ADD COLUMN email text;\n", outputBuffer.String())
}

func TestRunDiffLiveAddsNullableSQLiteInlineForeignKeyColumn(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "application.db")
	database, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE parents (id INTEGER PRIMARY KEY); CREATE TABLE children (id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, target, "tables/children.sql", "CREATE TABLE children (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parents(id));\n")
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{
		"diff-live",
		"-dialect", "sqlite",
		"-dsn", dsn,
		"-table", "children",
		"-to", target,
	}))
	require.Equal(t, "-- 001_add_column_children_parent_id.sql: add column children.parent_id\nALTER TABLE children ADD COLUMN parent_id integer REFERENCES parents (id);\n", outputBuffer.String())
}

func TestRunDiffLiveInspectsMixedCaseSQLiteTable(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "application.db")
	database, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE Members (id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE \"Members\" (id INTEGER PRIMARY KEY);\n")
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{
		"diff-live",
		"-dialect", "sqlite",
		"-dsn", dsn,
		"-table", "Members",
		"-to", target,
	}))
	require.Equal(t, "no schema changes\n", outputBuffer.String())
}

func TestRunDiffLiveMatchesMixedCaseSQLiteTableWhenSelectedLowercase(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "application.db")
	database, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE Members (id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id INTEGER PRIMARY KEY, email TEXT);\n")
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{
		"diff-live",
		"-dialect", "sqlite",
		"-dsn", dsn,
		"-table", "members",
		"-to", target,
	}))
	require.Equal(t, "-- 001_add_column_members_email.sql: add column members.email\nALTER TABLE members ADD COLUMN email text;\n", outputBuffer.String())
}

func TestRunDiffLivePreservesSQLiteForeignKeys(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "application.db")
	database, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE parents (id INTEGER PRIMARY KEY); CREATE TABLE children (parent_id INTEGER REFERENCES parents(id) ON DELETE CASCADE ON UPDATE CASCADE)`)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, target, "tables/children.sql", "CREATE TABLE children (parent_id INTEGER REFERENCES parents(id) ON DELETE CASCADE ON UPDATE CASCADE);\n")
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{
		"diff-live",
		"-dialect", "sqlite",
		"-dsn", dsn,
		"-table", "children",
		"-to", target,
	}))
	require.Equal(t, "no schema changes\n", outputBuffer.String())
}

func TestRunDiffLivePreservesMultipleSQLiteForeignKeys(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "application.db")
	database, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `
		CREATE TABLE parent_a (id INTEGER PRIMARY KEY);
		CREATE TABLE parent_b (id INTEGER PRIMARY KEY);
		CREATE TABLE multi (
			a_id INTEGER REFERENCES parent_a(id) ON DELETE CASCADE,
			b_id INTEGER REFERENCES parent_b(id) ON UPDATE CASCADE
		);
	`)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, target, "tables/multi.sql", `
		CREATE TABLE multi (
			a_id INTEGER REFERENCES parent_a(id) ON DELETE CASCADE,
			b_id INTEGER REFERENCES parent_b(id) ON UPDATE CASCADE
		);
	`)
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{
		"diff-live",
		"-dialect", "sqlite",
		"-dsn", dsn,
		"-table", "multi",
		"-to", target,
	}))
	require.Equal(t, "no schema changes\n", outputBuffer.String())
}

func TestRunDiffLivePreservesSQLiteIndexes(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "application.db")
	database, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT NOT NULL); CREATE INDEX members_name_idx ON members (name)`)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT NOT NULL);\n")
	writeTestSchema(t, target, "indexes/members_name.sql", "CREATE INDEX members_name_idx ON members (name);\n")
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{
		"diff-live",
		"-dialect", "sqlite",
		"-dsn", dsn,
		"-table", "members",
		"-to", target,
	}))
	require.Equal(t, "no schema changes\n", outputBuffer.String())
}

func TestRunDiffLiveNormalizesSQLiteInlineConstraints(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "application.db")
	database, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE members (id INTEGER PRIMARY KEY, email TEXT UNIQUE CHECK (length(email) > 0))`)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id INTEGER PRIMARY KEY, email TEXT CHECK (length(email) > 0) UNIQUE);\n")
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{
		"diff-live",
		"-dialect", "sqlite",
		"-dsn", dsn,
		"-table", "members",
		"-to", target,
	}))
	require.Equal(t, "no schema changes\n", outputBuffer.String())
}

func TestRunDiffLiveMatchesSQLiteIndexNameCase(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "application.db")
	database, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT); CREATE INDEX Members_Name_IDX ON members (name)`)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT);\n")
	writeTestSchema(t, target, "indexes/members_name.sql", "CREATE INDEX members_name_idx ON members (name);\n")
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{
		"diff-live",
		"-dialect", "sqlite",
		"-dsn", dsn,
		"-table", "members",
		"-to", target,
	}))
	require.Equal(t, "no schema changes\n", outputBuffer.String())
}

func TestRunDiffLiveRejectsExtraSQLiteTargetTable(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "application.db")
	database, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE members (id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id INTEGER PRIMARY KEY);\n")
	writeTestSchema(t, target, "tables/audit.sql", "CREATE TABLE audit (id INTEGER PRIMARY KEY);\n")
	outputBuffer := setCommandOutput(t)
	err = run([]string{
		"diff-live",
		"-dialect", "sqlite",
		"-dsn", dsn,
		"-table", "members",
		"-to", target,
	})
	require.ErrorContains(t, err, `diff-live target contains table "audit"`)
	require.Empty(t, outputBuffer.String())
}

func TestRunDiffLiveRejectsSQLiteIndexForExtraTargetTable(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "application.db")
	database, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE members (id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id INTEGER PRIMARY KEY);\n")
	writeTestSchema(t, target, "indexes/audit_name.sql", "CREATE INDEX audit_name_idx ON audit (name);\n")
	outputBuffer := setCommandOutput(t)
	err = run([]string{
		"diff-live",
		"-dialect", "sqlite",
		"-dsn", dsn,
		"-table", "members",
		"-to", target,
	})
	require.ErrorContains(t, err, `parse target desired schema: sqlite schema source "indexes/audit_name.sql" defines index audit_name_idx on missing table audit`)
	require.Empty(t, outputBuffer.String())
}

func TestRunDiffLiveRefusesDestructiveChange(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "application.db")
	database, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE members (id INTEGER PRIMARY KEY, email TEXT)`)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id INTEGER NOT NULL, name TEXT NOT NULL, PRIMARY KEY (id));\n")
	outputBuffer := setCommandOutput(t)
	err = run([]string{
		"diff-live",
		"-dialect", "sqlite",
		"-dsn", dsn,
		"-table", "members",
		"-to", target,
	})
	require.ErrorContains(t, err, "column members.email was removed")
	require.Empty(t, outputBuffer.String())
}

func TestRunDiffLiveHonorsInspectionTimeout(t *testing.T) {
	previousOpenDatabase := openDatabase
	previousTimeout := liveInspectionTimeout
	t.Cleanup(func() {
		openDatabase = previousOpenDatabase
		liveInspectionTimeout = previousTimeout
	})
	openDatabase = func(string, string) (*sql.DB, error) {
		return sql.Open(blockingInspectionDriverName, "")
	}
	liveInspectionTimeout = 20 * time.Millisecond

	started := time.Now()
	err := run([]string{
		"diff-live",
		"-dialect", "sqlite",
		"-dsn", "blocked",
		"-table", "members",
		"-to", t.TempDir(),
	})

	require.ErrorContains(t, err, "context deadline exceeded")
	require.Less(t, time.Since(started), time.Second)
}

func TestRunDiffLiveSkipsBlockingCleanupAfterTimeout(t *testing.T) {
	previousOpenDatabase := openDatabase
	previousTimeout := liveInspectionTimeout
	state := &nonCooperativeInspectionState{release: make(chan struct{}), queryStarted: make(chan struct{}), queryFinished: make(chan struct{})}
	nonCooperativeInspectionStateForTest = state
	t.Cleanup(func() {
		state.releaseOperation()
		nonCooperativeInspectionStateForTest = nil
		openDatabase = previousOpenDatabase
		liveInspectionTimeout = previousTimeout
	})
	openDatabase = func(string, string) (*sql.DB, error) {
		return sql.Open(nonCooperativeInspectionDriverName, "")
	}
	liveInspectionTimeout = time.Second

	started := time.Now()
	err := run([]string{
		"diff-live",
		"-dialect", "sqlite",
		"-dsn", "blocked",
		"-table", "members",
		"-to", t.TempDir(),
	})

	require.ErrorContains(t, err, "context deadline exceeded")
	require.Less(t, time.Since(started), 2*time.Second)
	require.Zero(t, state.rollback.Load())
	require.Zero(t, state.closed.Load())
	state.releaseOperation()
	select {
	case <-state.queryFinished:
	case <-time.After(time.Second):
		t.Fatal("non-cooperative query did not finish after release")
	}
}

func TestRunDiffLiveInspectsWithinOneTransaction(t *testing.T) {
	previousOpenDatabase := openDatabase
	state := &snapshotInspectionState{}
	snapshotInspectionStateForTest = state
	t.Cleanup(func() {
		openDatabase = previousOpenDatabase
		snapshotInspectionStateForTest = nil
	})
	openDatabase = func(string, string) (*sql.DB, error) {
		return sql.Open(snapshotInspectionDriverName, "")
	}

	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id INTEGER PRIMARY KEY);\n")
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{
		"diff-live",
		"-dialect", "sqlite",
		"-dsn", "snapshot",
		"-table", "members",
		"-to", target,
	}))
	require.Equal(t, "no schema changes\n", outputBuffer.String())
	require.Equal(t, 0, state.queriesOutsideTransaction)
	require.True(t, state.transactionStarted)
	require.True(t, state.transactionReadOnly)
}

func TestLiveInspectionTxOptionsUseRepeatableReadWhereSupported(t *testing.T) {
	for _, test := range []struct {
		dialect   string
		isolation sql.IsolationLevel
	}{
		{dialect: "postgresql", isolation: sql.LevelRepeatableRead},
		{dialect: "mysql", isolation: sql.LevelRepeatableRead},
		{dialect: "sqlite", isolation: sql.LevelDefault},
	} {
		t.Run(test.dialect, func(t *testing.T) {
			options := liveInspectionTxOptions(test.dialect)
			require.True(t, options.ReadOnly)
			require.Equal(t, test.isolation, options.Isolation)
		})
	}
}

func TestLiveSchemaAnalyzerUsesMySQLTableNameMode(t *testing.T) {
	for _, mode := range []mysql.LowerCaseTableNames{
		mysql.LowerCaseTableNamesLowercase,
		mysql.LowerCaseTableNamesPreserve,
	} {
		t.Run(fmt.Sprintf("lower_case_table_names=%d", mode), func(t *testing.T) {
			database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, database.Close())
				require.NoError(t, mock.ExpectationsWereMet())
			})
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT @@lower_case_table_names").
				WillReturnRows(sqlmock.NewRows([]string{"@@lower_case_table_names"}).AddRow(int64(mode)))
			mock.ExpectRollback()
			mock.ExpectClose()

			transaction, err := database.Begin()
			require.NoError(t, err)
			analyzer, err := liveSchemaAnalyzer(t.Context(), transaction, "mysql")
			require.NoError(t, err)
			baseline, err := analyzer.Parse([]diff.Source{{Path: "baseline.sql", SQL: "CREATE TABLE members (id bigint);"}})
			require.NoError(t, err)
			target, err := analyzer.Parse([]diff.Source{{Path: "target.sql", SQL: "CREATE TABLE Members (id bigint, email text);"}})
			require.NoError(t, err)
			plan, err := analyzer.Diff(baseline, target)
			require.NoError(t, err)
			require.Len(t, plan.Statements, 1)
			require.Contains(t, plan.Statements[0].SQL, "ADD COLUMN")

			require.NoError(t, transaction.Rollback())
		})
	}
}

func TestLiveSchemaAnalyzerHonorsMySQLModeTimeout(t *testing.T) {
	state := &nonCooperativeInspectionState{release: make(chan struct{}), queryStarted: make(chan struct{}), queryFinished: make(chan struct{})}
	nonCooperativeInspectionStateForTest = state
	database, err := sql.Open(nonCooperativeInspectionDriverName, "")
	require.NoError(t, err)
	t.Cleanup(func() {
		state.releaseOperation()
		nonCooperativeInspectionStateForTest = nil
		require.NoError(t, database.Close())
	})
	transaction, err := database.Begin()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	_, err = liveSchemaAnalyzer(ctx, transaction, "mysql")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	state.releaseOperation()
	select {
	case <-state.queryFinished:
	case <-time.After(time.Second):
		t.Fatal("non-cooperative MySQL metadata query did not finish after release")
	}
	require.NoError(t, transaction.Rollback())
}

func TestRunWithHardDeadlineReturnsContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()

	_, err := runWithHardDeadline(ctx, func() (struct{}, error) {
		<-ctx.Done()
		return struct{}{}, ctx.Err()
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestRunPlanPrintsSQLSources(t *testing.T) {
	directory := newTestDirectory(t)
	writeTestMigration(t, directory, "001_create_users")
	writeTestSQL(t, directory, "001_create_users", "002_users_email_index.up.sql", "CREATE INDEX \"users_email_idx\" ON \"users\" (\"email\");\n")
	writeTestSQL(t, directory, "001_create_users", "002_users_email_index.down.sql", "DROP INDEX \"users_email_idx\";\n")
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{"plan", "-dir", directory}))
	require.Equal(t, "-- 001_create_users/001_create_users.up.sql\nCREATE TABLE \"users\" (\"id\" INTEGER PRIMARY KEY);\n\n-- 001_create_users/002_users_email_index.up.sql\nCREATE INDEX \"users_email_idx\" ON \"users\" (\"email\");\n", outputBuffer.String())
}

func TestRunApplyStatusAndVerifySQLiteSQLSources(t *testing.T) {
	directory := newTestDirectory(t)
	writeTestMigration(t, directory, "001_create_users")
	dsn := filepath.Join(t.TempDir(), "application.db")
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{"apply", "-dir", directory, "-dialect", "sqlite", "-dsn", dsn}))
	require.Equal(t, "migration apply completed\n", outputBuffer.String())

	outputBuffer.Reset()
	require.NoError(t, run([]string{"status", "-dir", directory, "-dialect", "sqlite", "-dsn", dsn}))
	require.Equal(t, "applied\t001_create_users\n", outputBuffer.String())

	outputBuffer.Reset()
	require.NoError(t, run([]string{"verify", "-dir", directory, "-dialect", "sqlite", "-dsn", dsn}))
	require.Equal(t, "migration verification passed\n", outputBuffer.String())
}

// TestRunDownRevertsAppliedMigrations drives the command end to end
// against SQLite: apply two migrations, revert the newest, and see the
// history and the schema both follow.
func TestRunDownRevertsAppliedMigrations(t *testing.T) {
	directory := newTestDirectory(t)
	writeTestMigration(t, directory, "001_create_users")
	writeTestSQL(t, directory, "002_create_posts", "001_create_posts.up.sql", "CREATE TABLE \"posts\" (\"id\" INTEGER PRIMARY KEY);\n")
	writeTestSQL(t, directory, "002_create_posts", "001_create_posts.down.sql", "DROP TABLE \"posts\";\n")
	dsn := filepath.Join(t.TempDir(), "application.db")
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{"apply", "-dir", directory, "-dialect", "sqlite", "-dsn", dsn}))

	outputBuffer.Reset()
	require.NoError(t, run([]string{"down", "-dir", directory, "-dialect", "sqlite", "-dsn", dsn, "-steps", "1"}))
	require.Equal(t, "reverted\t002_create_posts\nmigration revert completed: 1 reverted\n", outputBuffer.String())

	outputBuffer.Reset()
	require.NoError(t, run([]string{"status", "-dir", directory, "-dialect", "sqlite", "-dsn", dsn}))
	require.Equal(t, "applied\t001_create_users\npending\t002_create_posts\n", outputBuffer.String())
}

// TestRunDownToLeavesTheNamedMigrationApplied pins what -to means, which is
// the flag most easily read as "revert this one too".
func TestRunDownToLeavesTheNamedMigrationApplied(t *testing.T) {
	directory := newTestDirectory(t)
	writeTestMigration(t, directory, "001_create_users")
	writeTestSQL(t, directory, "002_create_posts", "001_create_posts.up.sql", "CREATE TABLE \"posts\" (\"id\" INTEGER PRIMARY KEY);\n")
	writeTestSQL(t, directory, "002_create_posts", "001_create_posts.down.sql", "DROP TABLE \"posts\";\n")
	dsn := filepath.Join(t.TempDir(), "application.db")
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{"apply", "-dir", directory, "-dialect", "sqlite", "-dsn", dsn}))

	outputBuffer.Reset()
	require.NoError(t, run([]string{"down", "-dir", directory, "-dialect", "sqlite", "-dsn", dsn, "-to", "001_create_users"}))
	require.Equal(t, "reverted\t002_create_posts\nmigration revert completed: 1 reverted\n", outputBuffer.String())
}

func TestRunDownDryRunPrintsReverseSQLAndChangesNothing(t *testing.T) {
	directory := newTestDirectory(t)
	writeTestMigration(t, directory, "001_create_users")
	dsn := filepath.Join(t.TempDir(), "application.db")
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{"apply", "-dir", directory, "-dialect", "sqlite", "-dsn", dsn}))

	outputBuffer.Reset()
	require.NoError(t, run([]string{"down", "-dir", directory, "-dialect", "sqlite", "-dsn", dsn, "-steps", "1", "-dry-run"}))
	require.Equal(t, "-- 001_create_users/001_create_users.down.sql\nDROP TABLE \"users\";\n", outputBuffer.String())

	outputBuffer.Reset()
	require.NoError(t, run([]string{"status", "-dir", directory, "-dialect", "sqlite", "-dsn", dsn}))
	require.Equal(t, "applied\t001_create_users\n", outputBuffer.String())
}

// TestRunDownRequiresExactlyOneTarget refuses both the run that names no
// target and the run that names two, so no invocation can revert a depth
// nobody asked for.
func TestRunDownRequiresExactlyOneTarget(t *testing.T) {
	directory := newTestDirectory(t)
	writeTestMigration(t, directory, "001_create_users")
	dsn := filepath.Join(t.TempDir(), "application.db")
	setCommandOutput(t)
	require.NoError(t, run([]string{"apply", "-dir", directory, "-dialect", "sqlite", "-dsn", dsn}))

	err := run([]string{"down", "-dir", directory, "-dialect", "sqlite", "-dsn", dsn})
	require.EqualError(t, err, "down requires -to or -steps")

	err = run([]string{"down", "-dir", directory, "-dialect", "sqlite", "-dsn", dsn, "-to", "001_create_users", "-steps", "1"})
	require.EqualError(t, err, "down accepts -to or -steps, not both")
}

func TestMigrationLoadingRejectsUnexpectedEntries(t *testing.T) {
	directory := newTestDirectory(t)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "001_create_users.sql"), []byte("CREATE TABLE users (id INTEGER);"), 0o600))
	_, err := migrationdir.Load(directory)
	require.ErrorContains(t, err, "non-directory entry")

	require.NoError(t, os.Remove(filepath.Join(directory, "001_create_users.sql")))
	writeTestSQL(t, directory, "001_create_users", "001_create_users.txt", "CREATE TABLE users (id INTEGER);")
	_, err = migrationdir.Load(directory)
	require.ErrorContains(t, err, "which is neither a .up.sql nor a .down.sql source")
}

// writeTestMigration writes the one reversible migration most of these
// tests need: a create-table forward source and the drop that undoes it.
func writeTestMigration(t *testing.T, directory string, id string) {
	t.Helper()
	writeTestSQL(t, directory, id, "001_create_users.up.sql", "CREATE TABLE \"users\" (\"id\" INTEGER PRIMARY KEY);\n")
	writeTestSQL(t, directory, id, "001_create_users.down.sql", "DROP TABLE \"users\";\n")
}

func TestRunHelpAndUnknownCommand(t *testing.T) {
	outputBuffer := setCommandOutput(t)
	err := run([]string{"-h"})
	require.ErrorIs(t, err, flag.ErrHelp)
	require.Contains(t, outputBuffer.String(), "Usage: rasqlmigrate <command> [flags]")
	require.Error(t, run([]string{"unknown"}))
}

func newTestDirectory(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// setCommandOutput collects what a command writes. It takes both streams,
// so a diagnostic a test provokes lands in the buffer rather than on the
// test binary's own standard output.
func setCommandOutput(t *testing.T) *bytes.Buffer {
	t.Helper()
	previousOutput, previousDiagnostics := commandOutput, commandDiagnostics
	output := new(bytes.Buffer)
	commandOutput, commandDiagnostics = output, output
	t.Cleanup(func() {
		commandOutput, commandDiagnostics = previousOutput, previousDiagnostics
	})
	return output
}

func writeTestSQL(t *testing.T, directory string, migrationID string, filename string, source string) {
	t.Helper()
	migrationDirectory := filepath.Join(directory, migrationID)
	require.NoError(t, os.MkdirAll(migrationDirectory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(migrationDirectory, filename), []byte(source), 0o600))
}

func writeTestSchema(t *testing.T, directory string, filename string, source string) {
	t.Helper()
	path := filepath.Join(directory, filename)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
}

const blockingInspectionDriverName = "rasqlmigrate-blocking-inspection"

func init() {
	sql.Register(blockingInspectionDriverName, blockingInspectionDriver{})
}

type blockingInspectionDriver struct{}

func (blockingInspectionDriver) Open(string) (driver.Conn, error) {
	return blockingInspectionConn{}, nil
}

type blockingInspectionConn struct{}

func (blockingInspectionConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("blocking inspection driver does not prepare statements")
}

func (blockingInspectionConn) Close() error {
	return nil
}

func (blockingInspectionConn) Begin() (driver.Tx, error) {
	return nil, errors.New("blocking inspection driver does not begin transactions")
}

func (blockingInspectionConn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (blockingInspectionConn) Ping(context.Context) error {
	return nil
}

func (blockingInspectionConn) QueryContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

var _ driver.Driver = blockingInspectionDriver{}
var _ driver.Conn = blockingInspectionConn{}
var _ driver.ConnBeginTx = blockingInspectionConn{}
var _ driver.Pinger = blockingInspectionConn{}
var _ driver.QueryerContext = blockingInspectionConn{}

const nonCooperativeInspectionDriverName = "rasqlmigrate-non-cooperative-inspection"

var nonCooperativeInspectionStateForTest *nonCooperativeInspectionState

func init() {
	sql.Register(nonCooperativeInspectionDriverName, nonCooperativeInspectionDriver{})
}

type nonCooperativeInspectionDriver struct{}

func (nonCooperativeInspectionDriver) Open(string) (driver.Conn, error) {
	return &nonCooperativeInspectionConn{state: nonCooperativeInspectionStateForTest}, nil
}

type nonCooperativeInspectionConn struct {
	state *nonCooperativeInspectionState
}

func (nonCooperativeInspectionConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("non-cooperative inspection driver does not prepare statements")
}

func (c *nonCooperativeInspectionConn) Close() error {
	c.state.closed.Add(1)
	return nil
}

func (c *nonCooperativeInspectionConn) Begin() (driver.Tx, error) {
	return c.begin(), nil
}

func (c *nonCooperativeInspectionConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.begin(), nil
}

func (c *nonCooperativeInspectionConn) begin() driver.Tx {
	return nonCooperativeInspectionTx{state: c.state}
}

func (nonCooperativeInspectionConn) Ping(context.Context) error {
	return nil
}

func (c *nonCooperativeInspectionConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	select {
	case <-c.state.queryStarted:
	default:
		close(c.state.queryStarted)
	}
	<-c.state.release
	close(c.state.queryFinished)
	return nil, errors.New("non-cooperative inspection query released")
}

type nonCooperativeInspectionTx struct {
	state *nonCooperativeInspectionState
}

func (tx nonCooperativeInspectionTx) Commit() error {
	return nil
}

func (tx nonCooperativeInspectionTx) Rollback() error {
	tx.state.rollback.Add(1)
	return nil
}

type nonCooperativeInspectionState struct {
	closed        atomic.Int32
	rollback      atomic.Int32
	release       chan struct{}
	queryStarted  chan struct{}
	queryFinished chan struct{}
	releaseOnce   sync.Once
}

func (s *nonCooperativeInspectionState) releaseOperation() {
	s.releaseOnce.Do(func() { close(s.release) })
}

var _ driver.Driver = nonCooperativeInspectionDriver{}
var _ driver.Conn = (*nonCooperativeInspectionConn)(nil)
var _ driver.ConnBeginTx = (*nonCooperativeInspectionConn)(nil)
var _ driver.Pinger = (*nonCooperativeInspectionConn)(nil)
var _ driver.QueryerContext = (*nonCooperativeInspectionConn)(nil)
var _ driver.Tx = nonCooperativeInspectionTx{}

const snapshotInspectionDriverName = "rasqlmigrate-snapshot-inspection"

var snapshotInspectionStateForTest *snapshotInspectionState

func init() {
	sql.Register(snapshotInspectionDriverName, snapshotInspectionDriver{})
}

type snapshotInspectionState struct {
	queriesOutsideTransaction int
	transactionStarted        bool
	transactionReadOnly       bool
}

type snapshotInspectionDriver struct{}

func (snapshotInspectionDriver) Open(string) (driver.Conn, error) {
	return &snapshotInspectionConn{state: snapshotInspectionStateForTest}, nil
}

type snapshotInspectionConn struct {
	state *snapshotInspectionState
	inTx  bool
}

func (*snapshotInspectionConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("snapshot inspection driver does not prepare statements")
}

func (*snapshotInspectionConn) Close() error {
	return nil
}

func (c *snapshotInspectionConn) Begin() (driver.Tx, error) {
	return c.begin(driver.TxOptions{})
}

func (c *snapshotInspectionConn) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	return c.begin(options)
}

func (c *snapshotInspectionConn) begin(options driver.TxOptions) (driver.Tx, error) {
	c.inTx = true
	c.state.transactionStarted = true
	c.state.transactionReadOnly = options.ReadOnly
	return snapshotInspectionTx{conn: c}, nil
}

func (*snapshotInspectionConn) Ping(context.Context) error {
	return nil
}

func (c *snapshotInspectionConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if !c.inTx {
		c.state.queriesOutsideTransaction++
		if c.state.queriesOutsideTransaction > 1 {
			return nil, errors.New("snapshot inspection metadata changed between pooled connections")
		}
	}
	return snapshotInspectionRows(query)
}

type snapshotInspectionTx struct {
	conn *snapshotInspectionConn
}

func (tx snapshotInspectionTx) Commit() error {
	tx.conn.inTx = false
	return nil
}

func (tx snapshotInspectionTx) Rollback() error {
	tx.conn.inTx = false
	return nil
}

func snapshotInspectionRows(query string) (driver.Rows, error) {
	switch query {
	case `PRAGMA table_list("members")`:
		return newSnapshotInspectionRows(
			[]string{"schema", "name", "type", "ncol", "wr", "strict"},
			[]driver.Value{"main", "members", "table", int64(1), int64(0), int64(0)},
		), nil
	case `PRAGMA "main".table_xinfo("members")`:
		return newSnapshotInspectionRows(
			[]string{"cid", "name", "type", "notnull", "dflt_value", "pk", "hidden"},
			[]driver.Value{int64(0), "id", "INTEGER", int64(0), nil, int64(1), int64(0)},
		), nil
	case `SELECT sql FROM "main".sqlite_master WHERE type = 'table' AND name = ?`:
		return newSnapshotInspectionRows([]string{"sql"}, []driver.Value{"CREATE TABLE members (id INTEGER PRIMARY KEY)"}), nil
	case `PRAGMA "main".index_list("members")`:
		return newSnapshotInspectionRows([]string{"seq", "name", "unique", "origin", "partial"}), nil
	case `PRAGMA "main".foreign_key_list("members")`:
		return newSnapshotInspectionRows([]string{"id", "seq", "table", "from", "to", "on_update", "on_delete", "match"}), nil
	default:
		return nil, fmt.Errorf("snapshot inspection driver received unexpected query %q", query)
	}
}

type snapshotInspectionResultRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func newSnapshotInspectionRows(columns []string, rows ...[]driver.Value) *snapshotInspectionResultRows {
	return &snapshotInspectionResultRows{columns: columns, values: rows}
}

func (r *snapshotInspectionResultRows) Columns() []string {
	return r.columns
}

func (*snapshotInspectionResultRows) Close() error {
	return nil
}

func (r *snapshotInspectionResultRows) Next(destination []driver.Value) error {
	if r.index == len(r.values) {
		return io.EOF
	}
	copy(destination, r.values[r.index])
	r.index++
	return nil
}

var _ driver.Driver = snapshotInspectionDriver{}
var _ driver.Conn = (*snapshotInspectionConn)(nil)
var _ driver.ConnBeginTx = (*snapshotInspectionConn)(nil)
var _ driver.Pinger = (*snapshotInspectionConn)(nil)
var _ driver.QueryerContext = (*snapshotInspectionConn)(nil)
var _ driver.Tx = snapshotInspectionTx{}
var _ driver.Rows = (*snapshotInspectionResultRows)(nil)
