package catalogread_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestReadTxKeepsCallerTransactionAndUncommittedDDL(t *testing.T) {
	db := openFileSQLite(t)
	conn1 := openSQLiteConn(t, db)
	conn2 := openSQLiteConn(t, db)

	tx, err := conn1.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = tx.ExecContext(t.Context(), "CREATE TABLE tx_visible (id INTEGER PRIMARY KEY, payload TEXT NOT NULL)")
	require.NoError(t, err)

	result, err := catalogread.ReadTx(t.Context(), tx, sqliteProfile(t), catalogread.Scope{})
	require.NoError(t, err)
	table := findTable(t, result.Tables, "tx_visible")
	require.Equal(t, []string{"id", "payload"}, columnNames(table))

	// Mutating one returned descriptor must not alter a later read's snapshot.
	result.Tables[0].Columns[0].Name = "caller_mutation"
	_, err = tx.ExecContext(t.Context(), "CREATE TABLE tx_after_read (id INTEGER)")
	require.NoError(t, err)
	var count int
	require.NoError(t, tx.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE name IN ('tx_visible', 'tx_after_read')").Scan(&count))
	require.Equal(t, 2, count)
	require.NoError(t, tx.Rollback())

	assertTableAbsent(t, conn2, "tx_visible")
	assertTableAbsent(t, conn2, "tx_after_read")

	tx, err = conn1.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(t.Context(), "CREATE TABLE tx_error (id INTEGER)")
	require.NoError(t, err)
	_, err = catalogread.ReadTx(t.Context(), tx, sqliteProfile(t), catalogread.Scope{
		Include: []schema.ObjectName{{Name: "missing_from_tx"}},
	})
	require.ErrorIs(t, err, catalogread.ErrUnresolvedFact)
	_, err = tx.ExecContext(t.Context(), "CREATE TABLE tx_after_error (id INTEGER)")
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	assertTableAbsent(t, conn2, "tx_error")
	assertTableAbsent(t, conn2, "tx_after_error")
}

func TestReadConnKeepsRawImmediateTransactionAndUncommittedDDL(t *testing.T) {
	db := openFileSQLite(t)
	conn1 := openSQLiteConn(t, db)
	conn2 := openSQLiteConn(t, db)

	beginImmediate(t, conn1)
	_, err := conn1.ExecContext(t.Context(), "CREATE TABLE conn_visible (id INTEGER PRIMARY KEY, payload TEXT NOT NULL)")
	require.NoError(t, err)

	result, err := catalogread.ReadConn(t.Context(), conn1, sqliteProfile(t), catalogread.Scope{})
	require.NoError(t, err)
	table := findTable(t, result.Tables, "conn_visible")
	require.Equal(t, []string{"id", "payload"}, columnNames(table))

	_, err = conn2.ExecContext(t.Context(), "PRAGMA busy_timeout = 0")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	_, err = conn2.ExecContext(ctx, "CREATE TABLE conn_blocked (id INTEGER)")
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "locked")

	_, err = conn1.ExecContext(t.Context(), "CREATE TABLE conn_after_read (id INTEGER)")
	require.NoError(t, err)
	_, err = conn1.ExecContext(t.Context(), "ROLLBACK")
	require.NoError(t, err)
	assertTableAbsent(t, conn2, "conn_visible")
	assertTableAbsent(t, conn2, "conn_after_read")

	beginImmediate(t, conn1)
	_, err = conn1.ExecContext(t.Context(), "CREATE TABLE conn_error (id INTEGER)")
	require.NoError(t, err)
	_, err = catalogread.ReadConn(t.Context(), conn1, sqliteProfile(t), catalogread.Scope{
		Include: []schema.ObjectName{{Name: "missing_from_conn"}},
	})
	require.ErrorIs(t, err, catalogread.ErrUnresolvedFact)
	_, err = conn1.ExecContext(t.Context(), "CREATE TABLE conn_after_error (id INTEGER)")
	require.NoError(t, err)
	_, err = conn1.ExecContext(t.Context(), "ROLLBACK")
	require.NoError(t, err)
	assertTableAbsent(t, conn2, "conn_error")
	assertTableAbsent(t, conn2, "conn_after_error")
}

func openFileSQLite(t *testing.T) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=rwc&_foreign_keys=on", t.TempDir()+"/catalog.db")
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(2)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func openSQLiteConn(t *testing.T, db *sql.DB) *sql.Conn {
	t.Helper()
	conn, err := db.Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	return conn
}

func beginImmediate(t *testing.T, conn *sql.Conn) {
	t.Helper()
	_, err := conn.ExecContext(t.Context(), "BEGIN IMMEDIATE")
	require.NoError(t, err)
}

func findTable(t *testing.T, tables []schema.TableDef, name string) schema.TableDef {
	t.Helper()
	for _, table := range tables {
		if table.Name == name {
			return table
		}
	}
	t.Fatalf("table %q was not inspected", name)
	return schema.TableDef{}
}

func columnNames(table schema.TableDef) []string {
	result := make([]string, len(table.Columns))
	for i := range table.Columns {
		result[i] = table.Columns[i].Name
	}
	return result
}

func assertTableAbsent(t *testing.T, conn *sql.Conn, name string) {
	t.Helper()
	var count int
	err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE name = ?", name).Scan(&count)
	require.NoError(t, err)
	require.Zero(t, count, "table %q remains visible after caller rollback", name)
}
