package sqliteerr_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/dberror"
	"github.com/lestrrat-go/rasql/dberror/sqliteerr"
	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"
)

func TestSQLiteConstraintMappings(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.ExecContext(t.Context(), `PRAGMA foreign_keys = ON;
CREATE TABLE parents (id INTEGER PRIMARY KEY);
CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT UNIQUE NOT NULL, parent_id INTEGER REFERENCES parents(id), score INTEGER CHECK (score > 0));
INSERT INTO parents (id) VALUES (1);
INSERT INTO users (id, email, parent_id, score) VALUES (1, 'ada@example.com', 1, 1);`)
	require.NoError(t, err)
	tests := []struct {
		name     string
		query    string
		category dberror.Category
		code     string
	}{
		{"unique", `INSERT INTO users (id, email, parent_id, score) VALUES (2, 'ada@example.com', 1, 1)`, dberror.UniqueViolation, "2067"},
		{"foreign key", `INSERT INTO users (id, email, parent_id, score) VALUES (2, 'bob@example.com', 99, 1)`, dberror.ForeignKeyViolation, "787"},
		{"not null", `INSERT INTO users (id, email, parent_id, score) VALUES (2, NULL, 1, 1)`, dberror.NotNullViolation, "1299"},
		{"check", `INSERT INTO users (id, email, parent_id, score) VALUES (2, 'bob@example.com', 1, 0)`, dberror.CheckViolation, "275"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			err := func() error { _, err := database.ExecContext(t.Context(), testCase.query); return err }()
			require.Error(t, err)
			metadata, ok := dberror.Classify(fmt.Errorf("write failed: %w", err), sqliteerr.New())
			require.True(t, ok)
			require.Equal(t, testCase.category, metadata.Category)
			require.Equal(t, testCase.code, metadata.NativeCode)
		})
	}
}

func TestSQLiteLockClassification(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "database.sqlite")
	first, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	second, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, first.Close())
		require.NoError(t, second.Close())
	})
	_, err = first.ExecContext(t.Context(), `CREATE TABLE locks (id INTEGER PRIMARY KEY, value TEXT)`)
	require.NoError(t, err)
	transaction, err := first.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, transaction.Rollback()) })
	_, err = transaction.ExecContext(t.Context(), `INSERT INTO locks (id, value) VALUES (1, 'held')`)
	require.NoError(t, err)
	_, err = second.ExecContext(t.Context(), `PRAGMA busy_timeout = 100`)
	require.NoError(t, err)
	_, err = second.ExecContext(t.Context(), `INSERT INTO locks (id, value) VALUES (2, 'blocked')`)
	require.Error(t, err)
	var native *sqlite.Error
	require.ErrorAs(t, err, &native)
	require.Contains(t, []int{5, 6, 517}, native.Code())
	metadata, ok := dberror.Classify(err, sqliteerr.New())
	require.True(t, ok)
	require.Equal(t, dberror.TransactionConflict, metadata.Category)
	require.Contains(t, []string{"5", "6", "517"}, metadata.NativeCode)
}
