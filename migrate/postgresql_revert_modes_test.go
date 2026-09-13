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
	appliedRows := sqlmock.NewRows([]string{"id", "checksum"}).AddRow(atomic.ID, postgreSQLTestChecksum(atomic)).AddRow(nontransactional.ID, postgreSQLTestChecksum(nontransactional))
	mock.ExpectExec(`SELECT pg_advisory_lock(hashtextextended($1, 0))`).WithArgs(history).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS "rasql_schema_migrations" ("id" TEXT NOT NULL PRIMARY KEY, "checksum" TEXT NOT NULL, "applied_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP)`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT "id", "checksum" FROM "rasql_schema_migrations" ORDER BY "id"`).WillReturnRows(appliedRows)
	mock.ExpectExec(string(nontransactional.Down[0].SQL)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`DELETE FROM "rasql_schema_migrations" WHERE "id" = $1`).WithArgs(nontransactional.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectBegin()
	mock.ExpectExec(string(atomic.Down[0].SQL)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`DELETE FROM "rasql_schema_migrations" WHERE "id" = $1`).WithArgs(atomic.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT pg_advisory_unlock(hashtextextended($1, 0))`).WithArgs(history).WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(true))

	completed, err := runner.Revert(t.Context(), Steps(2), atomic, nontransactional)
	require.NoError(t, err)
	require.Equal(t, []Migration{nontransactional, atomic}, completed)
}

func TestPostgreSQLRevertMixedModesStopsAtAFailedReverseSource(t *testing.T) {
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
	mock.ExpectExec(`SELECT pg_advisory_lock(hashtextextended($1, 0))`).WithArgs(history).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS "rasql_schema_migrations" ("id" TEXT NOT NULL PRIMARY KEY, "checksum" TEXT NOT NULL, "applied_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP)`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT "id", "checksum" FROM "rasql_schema_migrations" ORDER BY "id"`).WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}).AddRow(atomic.ID, postgreSQLTestChecksum(atomic)).AddRow(nontransactional.ID, postgreSQLTestChecksum(nontransactional)))
	mock.ExpectExec(string(nontransactional.Down[0].SQL)).WillReturnError(errors.New("reverse source failed"))
	mock.ExpectQuery(`SELECT pg_advisory_unlock(hashtextextended($1, 0))`).WithArgs(history).WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(true))

	reverted, err := runner.Revert(t.Context(), Steps(2), atomic, nontransactional)
	require.ErrorContains(t, err, `migrate: execute migration "002_nontransactional" reverse SQL source "DROP INDEX CONCURRENTLY nontransactional_idx": reverse source failed`)
	require.Empty(t, reverted, "the newest migration's reverse failed, so nothing was reverted and the atomic one below it never ran")
	require.NoError(t, mock.ExpectationsWereMet())
}
