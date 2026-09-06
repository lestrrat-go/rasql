package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

func postgreSQLTestMigration(id, up, down string, mode ExecutionMode) Migration {
	return Migration{ID: id, Mode: mode, Statements: []Statement{{Source: up, SQL: sqltext.Text(up)}}, Down: []Statement{{Source: down, SQL: sqltext.Text(down)}}}
}

func postgreSQLTestChecksum(migration Migration) string {
	hash := sha256.New()
	if migration.Mode == ExecutionModeNonTransactional {
		_, _ = hash.Write([]byte("rasql-execution-mode\x00nontransactional\x00"))
	}
	for _, statement := range migration.Statements {
		_, _ = hash.Write([]byte(statement.Source))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(statement.SQL))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func TestPostgreSQLRevertMixedModesKeepsOneLockAndReversesSafely(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	runner, err := New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	atomic := postgreSQLTestMigration("001_atomic", "CREATE TABLE atomic_table (id BIGINT)", "DROP TABLE atomic_table", ExecutionModeAtomic)
	nontransactional := postgreSQLTestMigration("002_nontransactional", "CREATE INDEX CONCURRENTLY nontransactional_idx ON atomic_table (id)", "DROP INDEX CONCURRENTLY nontransactional_idx", ExecutionModeNonTransactional)
	history := `"rasql_schema_migrations"`
	progress := `"rasql_schema_migrations_progress"`
	progressRows := sqlmock.NewRows([]string{"id", "checksum", "direction", "source_index", "source", "next_index"})
	appliedRows := sqlmock.NewRows([]string{"id", "checksum"}).AddRow(atomic.ID, postgreSQLTestChecksum(atomic)).AddRow(nontransactional.ID, postgreSQLTestChecksum(nontransactional))
	mock.ExpectExec(`SELECT pg_advisory_lock(hashtextextended($1, 0))`).WithArgs(history).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS "rasql_schema_migrations" ("id" TEXT NOT NULL PRIMARY KEY, "checksum" TEXT NOT NULL, "applied_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP)`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT "id", "checksum" FROM "rasql_schema_migrations" ORDER BY "id"`).WillReturnRows(appliedRows)
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS "rasql_schema_migrations_progress" ("id" VARCHAR(255) NOT NULL PRIMARY KEY, "checksum" CHAR(64) NOT NULL, "direction" VARCHAR(16) NOT NULL, "source_index" INTEGER NOT NULL, "source" TEXT NOT NULL, "next_index" INTEGER NOT NULL, "started_at" TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT "id", "checksum", "direction", "source_index", "source", "next_index" FROM "rasql_schema_migrations_progress" ORDER BY "id"`).WillReturnRows(progressRows)
	mock.ExpectQuery(`SELECT "id", "checksum" FROM "rasql_schema_migrations" ORDER BY "id"`).WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}).AddRow(atomic.ID, postgreSQLTestChecksum(atomic)).AddRow(nontransactional.ID, postgreSQLTestChecksum(nontransactional)))
	mock.ExpectExec(`INSERT INTO "rasql_schema_migrations_progress" ("id", "checksum", "direction", "source_index", "source", "next_index") VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT ("id") DO UPDATE SET "checksum"=EXCLUDED."checksum", "direction"=EXCLUDED."direction", "source_index"=EXCLUDED."source_index", "source"=EXCLUDED."source", "next_index"=EXCLUDED."next_index"`).WithArgs(nontransactional.ID, postgreSQLTestChecksum(nontransactional), DirectionDown, 0, nontransactional.Down[0].Source, 0).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(string(nontransactional.Down[0].SQL)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`UPDATE "rasql_schema_migrations_progress" SET "next_index"=$1, "source"=$2 WHERE "id"=$3`).WithArgs(1, nontransactional.Down[0].Source, nontransactional.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT "id", "checksum" FROM "rasql_schema_migrations" ORDER BY "id"`).WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}).AddRow(atomic.ID, postgreSQLTestChecksum(atomic)).AddRow(nontransactional.ID, postgreSQLTestChecksum(nontransactional)))
	mock.ExpectExec(`DELETE FROM "rasql_schema_migrations" WHERE "id" = $1`).WithArgs(nontransactional.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM ` + progress + ` WHERE "id"=$1`).WithArgs(nontransactional.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectBegin()
	mock.ExpectExec(string(atomic.Down[0].SQL)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`DELETE FROM "rasql_schema_migrations" WHERE "id" = $1`).WithArgs(atomic.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT pg_advisory_unlock(hashtextextended($1, 0))`).WithArgs(history).WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(true))

	completed, err := runner.Revert(t.Context(), Steps(2), atomic, nontransactional)
	require.NoError(t, err)
	require.Equal(t, []Migration{nontransactional, atomic}, completed)
}

func TestPostgreSQLRevertMixedModesStopsAtIncompleteSource(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	runner, err := New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	atomic := postgreSQLTestMigration("001_atomic", "CREATE TABLE atomic_table (id BIGINT)", "DROP TABLE atomic_table", ExecutionModeAtomic)
	nontransactional := postgreSQLTestMigration("002_nontransactional", "CREATE INDEX CONCURRENTLY nontransactional_idx ON atomic_table (id)", "DROP INDEX CONCURRENTLY nontransactional_idx", ExecutionModeNonTransactional)
	history := `"rasql_schema_migrations"`
	previousHook := journalWriteHook
	journalWriteHook = func(operation string) error {
		if operation == "checkpoint" {
			return errors.New("checkpoint stopped")
		}
		return nil
	}
	t.Cleanup(func() { journalWriteHook = previousHook })
	mock.ExpectExec(`SELECT pg_advisory_lock(hashtextextended($1, 0))`).WithArgs(history).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS "rasql_schema_migrations" ("id" TEXT NOT NULL PRIMARY KEY, "checksum" TEXT NOT NULL, "applied_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP)`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT "id", "checksum" FROM "rasql_schema_migrations" ORDER BY "id"`).WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}).AddRow(atomic.ID, postgreSQLTestChecksum(atomic)).AddRow(nontransactional.ID, postgreSQLTestChecksum(nontransactional)))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS "rasql_schema_migrations_progress" ("id" VARCHAR(255) NOT NULL PRIMARY KEY, "checksum" CHAR(64) NOT NULL, "direction" VARCHAR(16) NOT NULL, "source_index" INTEGER NOT NULL, "source" TEXT NOT NULL, "next_index" INTEGER NOT NULL, "started_at" TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT "id", "checksum", "direction", "source_index", "source", "next_index" FROM "rasql_schema_migrations_progress" ORDER BY "id"`).WillReturnRows(sqlmock.NewRows([]string{"id", "checksum", "direction", "source_index", "source", "next_index"}))
	mock.ExpectQuery(`SELECT "id", "checksum" FROM "rasql_schema_migrations" ORDER BY "id"`).WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}).AddRow(atomic.ID, postgreSQLTestChecksum(atomic)).AddRow(nontransactional.ID, postgreSQLTestChecksum(nontransactional)))
	mock.ExpectExec(`INSERT INTO "rasql_schema_migrations_progress" ("id", "checksum", "direction", "source_index", "source", "next_index") VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT ("id") DO UPDATE SET "checksum"=EXCLUDED."checksum", "direction"=EXCLUDED."direction", "source_index"=EXCLUDED."source_index", "source"=EXCLUDED."source", "next_index"=EXCLUDED."next_index"`).WithArgs(nontransactional.ID, postgreSQLTestChecksum(nontransactional), DirectionDown, 0, nontransactional.Down[0].Source, 0).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(string(nontransactional.Down[0].SQL)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT pg_advisory_unlock(hashtextextended($1, 0))`).WithArgs(history).WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(true))

	result, err := runner.RevertResult(t.Context(), Steps(2), atomic, nontransactional)
	require.Error(t, err)
	var incomplete *IncompleteMigrationError
	require.ErrorAs(t, err, &incomplete)
	require.Empty(t, result.Completed)
	require.NotNil(t, result.Incomplete)
	require.Equal(t, nontransactional.ID, result.Incomplete.ID)
	require.Equal(t, DirectionDown, result.Incomplete.Direction)
	require.Equal(t, 0, result.Incomplete.SourceIndex)
	require.Equal(t, nontransactional.Down[0].Source, result.Incomplete.Source)
	require.NoError(t, mock.ExpectationsWereMet())
}
