package migrate_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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

	mock.ExpectExec(`SELECT pg_advisory_lock(hashtextextended($1, 0))`).
		WithArgs("\"rasql_schema_migrations\"").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS "rasql_schema_migrations" ("id" TEXT NOT NULL PRIMARY KEY, "checksum" TEXT NOT NULL, "applied_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT "id", "checksum" FROM "rasql_schema_migrations" ORDER BY "id"`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}))
	mock.ExpectBegin()
	mock.ExpectExec(string(migration.Statements[0].SQL)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO "rasql_schema_migrations" ("id", "checksum") VALUES ($1, $2)`).
		WithArgs("001_create_users", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT pg_advisory_unlock(hashtextextended($1, 0))`).
		WithArgs("\"rasql_schema_migrations\"").
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(true))

	requireApplied(t, t.Context(), runner, migration)
}

func TestRunnerUsesPostgreSQLMixedExecutionModesInOrder(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	runner, err := migrate.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	atomic := sqlMigration("001_atomic", "CREATE TABLE atomic_table (id BIGINT)")
	nontransactional := sqlMigration("002_nontransactional", "CREATE INDEX CONCURRENTLY nontransactional_idx ON atomic_table (id)")
	nontransactional.Mode = migrate.ExecutionModeNonTransactional
	hash := sha256.New()
	hash.Write([]byte(atomic.Statements[0].Source))
	hash.Write([]byte{0})
	hash.Write([]byte(atomic.Statements[0].SQL))
	hash.Write([]byte{0})
	checksumAtomic := hex.EncodeToString(hash.Sum(nil))
	history := `"rasql_schema_migrations"`
	mock.ExpectExec(`SELECT pg_advisory_lock(hashtextextended($1, 0))`).WithArgs(history).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS "rasql_schema_migrations" ("id" TEXT NOT NULL PRIMARY KEY, "checksum" TEXT NOT NULL, "applied_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP)`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT "id", "checksum" FROM "rasql_schema_migrations" ORDER BY "id"`).WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}))
	mock.ExpectBegin()
	mock.ExpectExec(string(atomic.Statements[0].SQL)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO "rasql_schema_migrations" ("id", "checksum") VALUES ($1, $2)`).WithArgs(atomic.ID, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS "rasql_schema_migrations_progress" ("id" VARCHAR(255) NOT NULL PRIMARY KEY, "checksum" CHAR(64) NOT NULL, "direction" VARCHAR(16) NOT NULL, "source_index" INTEGER NOT NULL, "source" TEXT NOT NULL, "next_index" INTEGER NOT NULL, "started_at" TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT "id", "checksum", "direction", "source_index", "source", "next_index" FROM "rasql_schema_migrations_progress" ORDER BY "id"`).WillReturnRows(sqlmock.NewRows([]string{"id", "checksum", "direction", "source_index", "source", "next_index"}))
	mock.ExpectQuery(`SELECT "id", "checksum" FROM "rasql_schema_migrations" ORDER BY "id"`).WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}).AddRow(atomic.ID, checksumAtomic))
	mock.ExpectExec(`INSERT INTO "rasql_schema_migrations_progress" ("id", "checksum", "direction", "source_index", "source", "next_index") VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT ("id") DO UPDATE SET "checksum"=EXCLUDED."checksum", "direction"=EXCLUDED."direction", "source_index"=EXCLUDED."source_index", "source"=EXCLUDED."source", "next_index"=EXCLUDED."next_index"`).WithArgs(nontransactional.ID, sqlmock.AnyArg(), "up", 0, nontransactional.Statements[0].Source, 0).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(string(nontransactional.Statements[0].SQL)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`UPDATE "rasql_schema_migrations_progress" SET "next_index"=$1, "source"=$2 WHERE "id"=$3`).WithArgs(1, nontransactional.Statements[0].Source, nontransactional.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT "id", "checksum" FROM "rasql_schema_migrations" ORDER BY "id"`).WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}).AddRow(atomic.ID, checksumAtomic))
	mock.ExpectExec(`INSERT INTO "rasql_schema_migrations" ("id", "checksum") VALUES ($1, $2)`).WithArgs(nontransactional.ID, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM "rasql_schema_migrations_progress" WHERE "id"=$1`).WithArgs(nontransactional.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT pg_advisory_unlock(hashtextextended($1, 0))`).WithArgs(history).WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(true))

	completed, err := runner.Apply(t.Context(), migrate.AllPending(), atomic, nontransactional)
	require.NoError(t, err)
	require.Equal(t, []migrate.Migration{atomic, nontransactional}, completed)
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
	migration.Mode = migrate.ExecutionModeNonTransactional

	mock.ExpectQuery("SELECT GET_LOCK(?, ?)").
		WithArgs("rasql_schema_migrations", 30).
		WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(1))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS `rasql_schema_migrations` (`id` VARCHAR(255) NOT NULL PRIMARY KEY, `checksum` CHAR(64) NOT NULL, `applied_at` TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT `id`, `checksum` FROM `rasql_schema_migrations` ORDER BY `id`").
		WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS `rasql_schema_migrations_progress` (`id` VARCHAR(255) NOT NULL PRIMARY KEY, `checksum` CHAR(64) NOT NULL, `direction` VARCHAR(16) NOT NULL, `source_index` INTEGER NOT NULL, `source` TEXT NOT NULL, `next_index` INTEGER NOT NULL, `started_at` TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT `id`, `checksum`, `direction`, `source_index`, `source`, `next_index` FROM `rasql_schema_migrations_progress` ORDER BY `id`").
		WillReturnRows(sqlmock.NewRows([]string{"id", "checksum", "direction", "source_index", "source", "next_index"}))
	mock.ExpectQuery("SELECT `id`, `checksum` FROM `rasql_schema_migrations` ORDER BY `id`").
		WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}))
	mock.ExpectExec("INSERT INTO `rasql_schema_migrations_progress` (`id`, `checksum`, `direction`, `source_index`, `source`, `next_index`) VALUES (?, ?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE `checksum`=VALUES(`checksum`), `direction`=VALUES(`direction`), `source_index`=VALUES(`source_index`), `source`=VALUES(`source`), `next_index`=VALUES(`next_index`)").
		WithArgs("001_create_users", sqlmock.AnyArg(), "up", 0, "001.sql", 0).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(string(migration.Statements[0].SQL)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("UPDATE `rasql_schema_migrations_progress` SET `next_index`=?, `source`=? WHERE `id`=?").
		WithArgs(1, "001.sql", "001_create_users").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT `id`, `checksum` FROM `rasql_schema_migrations` ORDER BY `id`").
		WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}))
	mock.ExpectExec("INSERT INTO `rasql_schema_migrations` (`id`, `checksum`) VALUES (?, ?)").
		WithArgs("001_create_users", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM `rasql_schema_migrations_progress` WHERE `id`=?").
		WithArgs("001_create_users").
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
