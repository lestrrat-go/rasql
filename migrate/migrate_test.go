package migrate_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestRunnerAppliesSQLiteMigrationsAndDetectsDrift(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)
	runner, err := migrate.New(database, dialect.SQLite())
	require.NoError(t, err)

	createUsers := sqlMigration("001_create_users", `CREATE TABLE "users" ("id" INTEGER PRIMARY KEY)`)
	addNickname := sqlMigration("002_add_nickname", `ALTER TABLE "users" ADD COLUMN "nickname" TEXT`)

	requireApplied(t, t.Context(), runner, addNickname, createUsers)
	requireApplied(t, t.Context(), runner, createUsers, addNickname)

	rows, err := database.QueryContext(t.Context(), "SELECT id, checksum FROM rasql_schema_migrations ORDER BY id")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, rows.Close())
	})
	var ids []string
	for rows.Next() {
		var id string
		var checksum string
		require.NoError(t, rows.Scan(&id, &checksum))
		require.Len(t, checksum, 64)
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{"001_create_users", "002_add_nickname"}, ids)

	drifted := addNickname
	drifted.Statements[0].Source = "002_display_name.sql"
	_, err = runner.Apply(t.Context(), migrate.AllPending(), createUsers, drifted)
	require.ErrorContains(t, err, "checksum does not match")
}

func TestRunnerRollsBackFailedSQLiteMigration(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)
	runner, err := migrate.New(database, dialect.SQLite())
	require.NoError(t, err)

	failing := sqlMigration("001_create_events",
		`CREATE TABLE "events" ("id" INTEGER PRIMARY KEY)`,
		`CREATE INDEX "events_id_idx" ON "events" ("id")`,
		`CREATE INDEX "events_id_idx" ON "events" ("id")`,
	)
	_, err = runner.Apply(t.Context(), migrate.AllPending(), failing)
	require.ErrorContains(t, err, "execute migration")

	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'events'").Scan(&count))
	require.Zero(t, count)
}

func TestRunnerRejectsRecordedMigrationAfterAMissingMigration(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)
	runner, err := migrate.New(database, dialect.SQLite())
	require.NoError(t, err)

	first := sqlMigration("001_create_users", `CREATE TABLE "users" ("id" INTEGER PRIMARY KEY)`)
	second := sqlMigration("002_add_nickname", `ALTER TABLE "users" ADD COLUMN "nickname" TEXT`)
	third := sqlMigration("003_add_display_name", `ALTER TABLE "users" ADD COLUMN "display_name" TEXT`)
	requireApplied(t, t.Context(), runner, first, third)
	_, err = runner.Apply(t.Context(), migrate.AllPending(), first, second, third)
	require.ErrorContains(t, err, "recorded after a missing migration")
}

func TestRunnerRejectsUnspecifiedRecordedMigration(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)
	runner, err := migrate.New(database, dialect.SQLite())
	require.NoError(t, err)
	first := sqlMigration("001_create_users", `CREATE TABLE "users" ("id" INTEGER PRIMARY KEY)`)
	second := sqlMigration("002_add_nickname", `ALTER TABLE "users" ADD COLUMN "nickname" TEXT`)
	requireApplied(t, t.Context(), runner, first, second)
	_, err = runner.Apply(t.Context(), migrate.AllPending(), first)
	require.ErrorContains(t, err, "was not supplied")
}

func TestRunnerStatusReportsPendingAppliedChangedAndUnknown(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)
	runner, err := migrate.New(database, dialect.SQLite())
	require.NoError(t, err)
	migration := sqlMigration("001_create_users", `CREATE TABLE "users" ("id" INTEGER PRIMARY KEY)`)

	status, err := runner.Status(t.Context(), migration)
	require.NoError(t, err)
	require.Equal(t, []migrate.StatusEntry{{ID: migration.ID, State: migrate.StatusPending}}, status)
	requireApplied(t, t.Context(), runner, migration)

	status, err = runner.Status(t.Context(), migration)
	require.NoError(t, err)
	require.Equal(t, []migrate.StatusEntry{{ID: migration.ID, State: migrate.StatusApplied}}, status)

	changed := migration
	changed.Statements[0].SQL = `CREATE TABLE "users" ("id" INTEGER PRIMARY KEY, "email" TEXT)`
	status, err = runner.Status(t.Context(), changed)
	require.NoError(t, err)
	require.Equal(t, []migrate.StatusEntry{{ID: migration.ID, State: migrate.StatusChanged}}, status)

	status, err = runner.Status(t.Context())
	require.NoError(t, err)
	require.Equal(t, []migrate.StatusEntry{{ID: migration.ID, State: migrate.StatusUnknown}}, status)
}

func TestRunnerUsesPostgreSQLTransactionAndHistoryLock(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	runner, err := migrate.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	migration := sqlMigration("001_create_users", `CREATE TABLE "users" ("id" BIGINT NOT NULL, PRIMARY KEY ("id"))`)

	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS "rasql_schema_migrations" ("id" TEXT NOT NULL PRIMARY KEY, "checksum" TEXT NOT NULL, "applied_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectBegin()
	mock.ExpectExec(`LOCK TABLE "rasql_schema_migrations" IN EXCLUSIVE MODE`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT "id", "checksum" FROM "rasql_schema_migrations" ORDER BY "id"`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}))
	mock.ExpectExec(string(migration.Statements[0].SQL)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO "rasql_schema_migrations" ("id", "checksum") VALUES ($1, $2)`).
		WithArgs("001_create_users", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	requireApplied(t, t.Context(), runner, migration)
}

func TestRunnerUsesMySQLConnectionLock(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	runner, err := migrate.New(database, dialect.MySQL())
	require.NoError(t, err)
	migration := sqlMigration("001_create_users", "CREATE TABLE `users` (`id` BIGINT NOT NULL, PRIMARY KEY (`id`))")

	mock.ExpectQuery("SELECT GET_LOCK(?, ?)").
		WithArgs("rasql_schema_migrations", 30).
		WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(1))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS `rasql_schema_migrations` (`id` VARCHAR(255) NOT NULL PRIMARY KEY, `checksum` CHAR(64) NOT NULL, `applied_at` TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT `id`, `checksum` FROM `rasql_schema_migrations` ORDER BY `id`").
		WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}))
	mock.ExpectExec(string(migration.Statements[0].SQL)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO `rasql_schema_migrations` (`id`, `checksum`) VALUES (?, ?)").
		WithArgs("001_create_users", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT RELEASE_LOCK(?)").
		WithArgs("rasql_schema_migrations").
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	requireApplied(t, t.Context(), runner, migration)
}

func TestRunnerRejectsInvalidSQLSource(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	runner, err := migrate.New(database, dialect.SQLite())
	require.NoError(t, err)
	_, err = runner.Apply(t.Context(), migrate.AllPending(), migrate.Migration{ID: "001", Statements: []migrate.Statement{{Source: "001.sql"}}})
	require.ErrorContains(t, err, "is empty")
}

func sqlMigration(id string, sqlSources ...string) migrate.Migration {
	statements := make([]migrate.Statement, len(sqlSources))
	for index, source := range sqlSources {
		statements[index] = migrate.Statement{
			Source: fmt.Sprintf("%03d.sql", index+1),
			SQL:    sqltext.Text(source),
		}
	}
	return migrate.Migration{ID: id, Statements: statements}
}

// requireApplied brings the database up to date and fails the test unless
// every supplied migration is reported as applied.
func requireApplied(t *testing.T, ctx context.Context, runner migrate.Runner, migrations ...migrate.Migration) []migrate.Migration {
	t.Helper()
	applied, err := runner.Apply(ctx, migrate.AllPending(), migrations...)
	require.NoError(t, err)
	return applied
}
