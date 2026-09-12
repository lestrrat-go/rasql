package compilerquery_test

import (
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// TestAnalyzePreparesAndClosesTheLoweredQuery pins the one database round trip analysis makes:
// PrepareContext on the lowered SQL, then Close, and nothing else. This is the whole of what the
// deleted querydescribe package did for SQLite and MySQL, and it is the only place a hand-written
// query meets the engine before it is checked in.
func TestAnalyzePreparesAndClosesTheLoweredQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectPrepare(regexp.QuoteMeta("SELECT id FROM users WHERE id = ?")).WillBeClosed()
	_, err = analyzeSQLiteQuery(t, db, `SELECT id FROM users WHERE id = {{bind "id" users.id}}`)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestAnalyzeRefusesSQLTheEngineCannotPrepare is why the prepare is kept: a misspelled column
// reaches no further than generation, and the engine's own message is what the user reads.
func TestAnalyzeRefusesSQLTheEngineCannotPrepare(t *testing.T) {
	_, err := analyzeSQLiteQuery(t, sqliteUsersDB(t), `SELECT no_such_column FROM users WHERE id = {{bind "id" users.id}}`)
	require.ErrorContains(t, err, "no such column: no_such_column")
}

// TestAnalyzeRefusesAMissingDatabase proves analysis never silently skips the prepare when it has
// no connection to make it on.
func TestAnalyzeRefusesAMissingDatabase(t *testing.T) {
	_, err := analyzeSQLiteQuery(t, nil, `SELECT id FROM users WHERE id = {{bind "id" users.id}}`)
	require.ErrorContains(t, err, "database connection is required")
}
