package catalogread_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
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
	_, err = tx.ExecContext(t.Context(), "CREATE TABLE tx_visible (left_key TEXT NOT NULL, right_key INTEGER NOT NULL, PRIMARY KEY (left_key, right_key)); CREATE INDEX tx_visible_idx ON tx_visible(right_key)")
	require.NoError(t, err)

	expected := schema.TableDef{Schema: "main", Name: "tx_visible", Kind: schema.ObjectTable,
		Columns:    []schema.ColumnDef{{Name: "left_key", Type: schema.TextType{}, Nullable: false}, {Name: "right_key", Type: schema.IntegerType{}, Nullable: false}},
		PrimaryKey: []string{"left_key", "right_key"}, Indexes: []schema.IndexDef{{Name: "tx_visible_idx", Columns: []string{"right_key"}}}}
	first, err := catalogread.ReadTx(t.Context(), tx, sqliteProfile(t), catalogread.Scope{})
	require.NoError(t, err)
	require.Equal(t, []schema.TableDef{expected}, first.Tables)

	second, err := catalogread.ReadTx(t.Context(), tx, sqliteProfile(t), catalogread.Scope{})
	require.NoError(t, err)
	secondIndex := tableIndex(t, second.Tables, "tx_visible")
	second.Tables[secondIndex].Name = "caller_mutation"
	second.Tables[secondIndex].Columns[0].Name = "changed_left"
	second.Tables[secondIndex].Columns[0].Type = schema.IntegerType{}
	second.Tables[secondIndex].PrimaryKey[0] = "changed_key"
	second.Tables[secondIndex].Indexes[0].Name = "changed_index"
	second.Tables[secondIndex].Indexes[0].Columns[0] = "changed_column"
	second.Tables = second.Tables[:0]
	second.Unresolved = append(second.Unresolved, catalogread.UnresolvedFact{Object: schema.ObjectName{Name: "mutated"}})
	require.Equal(t, expected, findTable(t, first.Tables, "tx_visible"))

	firstIndex := tableIndex(t, first.Tables, "tx_visible")
	first.Tables[firstIndex].Name = "caller_mutation"
	first.Tables[firstIndex].Columns[0].Name = "changed_again"
	first.Tables[firstIndex].PrimaryKey[0] = "changed_again_key"
	first.Tables[firstIndex].Indexes[0].Name = "changed_again_index"
	_, err = tx.ExecContext(t.Context(), "CREATE TABLE tx_after_read (id INTEGER)")
	require.NoError(t, err)
	third, err := catalogread.ReadTx(t.Context(), tx, sqliteProfile(t), catalogread.Scope{})
	require.NoError(t, err)
	require.Equal(t, expected, findTable(t, third.Tables, "tx_visible"))
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
	_, err := conn1.ExecContext(t.Context(), "CREATE TABLE conn_visible (left_key TEXT NOT NULL, right_key INTEGER NOT NULL, PRIMARY KEY (left_key, right_key)); CREATE INDEX conn_visible_idx ON conn_visible(right_key)")
	require.NoError(t, err)

	expected := schema.TableDef{Schema: "main", Name: "conn_visible", Kind: schema.ObjectTable,
		Columns:    []schema.ColumnDef{{Name: "left_key", Type: schema.TextType{}, Nullable: false}, {Name: "right_key", Type: schema.IntegerType{}, Nullable: false}},
		PrimaryKey: []string{"left_key", "right_key"}, Indexes: []schema.IndexDef{{Name: "conn_visible_idx", Columns: []string{"right_key"}}}}
	first, err := catalogread.ReadConn(t.Context(), conn1, sqliteProfile(t), catalogread.Scope{})
	require.NoError(t, err)
	require.Equal(t, []schema.TableDef{expected}, first.Tables)
	second, err := catalogread.ReadConn(t.Context(), conn1, sqliteProfile(t), catalogread.Scope{})
	require.NoError(t, err)
	secondIndex := tableIndex(t, second.Tables, "conn_visible")
	second.Tables[secondIndex].Name = "caller_mutation"
	second.Tables[secondIndex].Columns[0].Name = "changed_left"
	second.Tables[secondIndex].Columns[0].Type = schema.IntegerType{}
	second.Tables[secondIndex].PrimaryKey[0] = "changed_key"
	second.Tables[secondIndex].Indexes[0].Name = "changed_index"
	second.Tables[secondIndex].Indexes[0].Columns[0] = "changed_column"
	second.Tables = second.Tables[:0]
	second.Unresolved = append(second.Unresolved, catalogread.UnresolvedFact{Object: schema.ObjectName{Name: "mutated"}})
	require.Equal(t, expected, findTable(t, first.Tables, "conn_visible"))
	firstIndex := tableIndex(t, first.Tables, "conn_visible")
	first.Tables[firstIndex].Name = "caller_mutation"
	first.Tables[firstIndex].Columns[0].Name = "changed_again"
	first.Tables[firstIndex].PrimaryKey[0] = "changed_again_key"
	first.Tables[firstIndex].Indexes[0].Name = "changed_again_index"

	_, err = conn2.ExecContext(t.Context(), "PRAGMA busy_timeout = 0")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	_, err = conn2.ExecContext(ctx, "CREATE TABLE conn_blocked (id INTEGER)")
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "locked")

	_, err = conn1.ExecContext(t.Context(), "CREATE TABLE conn_after_read (id INTEGER)")
	require.NoError(t, err)
	third, err := catalogread.ReadConn(t.Context(), conn1, sqliteProfile(t), catalogread.Scope{})
	require.NoError(t, err)
	require.Equal(t, expected, findTable(t, third.Tables, "conn_visible"))
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

func TestReadQueryerConcurrentCallerOwnedReads(t *testing.T) {
	db := openFileSQLite(t)
	db.SetMaxOpenConns(16)
	_, err := db.ExecContext(t.Context(), "CREATE TABLE concurrent (left_key TEXT NOT NULL, right_key INTEGER NOT NULL, PRIMARY KEY (left_key, right_key)); CREATE INDEX concurrent_idx ON concurrent(right_key)")
	require.NoError(t, err)
	want := schema.TableDef{Schema: "main", Name: "concurrent", Kind: schema.ObjectTable,
		Columns:    []schema.ColumnDef{{Name: "left_key", Type: schema.TextType{}, Nullable: false}, {Name: "right_key", Type: schema.IntegerType{}, Nullable: false}},
		PrimaryKey: []string{"left_key", "right_key"}, Indexes: []schema.IndexDef{{Name: "concurrent_idx", Columns: []string{"right_key"}}}}
	start := make(chan struct{})
	type outcome struct {
		result catalogread.Result
		err    error
	}
	results := make(chan outcome, 8)
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func(useTx bool) {
			defer group.Done()
			<-start
			conn, connErr := db.Conn(t.Context())
			if connErr != nil {
				results <- outcome{err: connErr}
				return
			}
			defer func() { _ = conn.Close() }()
			if useTx {
				tx, beginErr := conn.BeginTx(t.Context(), nil)
				if beginErr != nil {
					results <- outcome{err: beginErr}
					return
				}
				result, readErr := catalogread.ReadTx(t.Context(), tx, sqliteProfile(t), catalogread.Scope{})
				_ = tx.Rollback()
				results <- outcome{result: result, err: readErr}
				return
			}
			result, readErr := catalogread.ReadConn(t.Context(), conn, sqliteProfile(t), catalogread.Scope{})
			results <- outcome{result: result, err: readErr}
		}(i%2 == 0)
	}
	close(start)
	group.Wait()
	close(results)
	for result := range results {
		require.NoError(t, result.err)
		require.Equal(t, []schema.TableDef{want}, result.result.Tables)
		require.Equal(t, sqliteProfile(t), result.result.Observed)
		require.Empty(t, result.result.Unresolved)
	}
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

func tableIndex(t *testing.T, tables []schema.TableDef, name string) int {
	t.Helper()
	for i, table := range tables {
		if table.Name == name {
			return i
		}
	}
	t.Fatalf("table %q was not inspected", name)
	return -1
}

func assertTableAbsent(t *testing.T, conn *sql.Conn, name string) {
	t.Helper()
	var count int
	err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE name = ?", name).Scan(&count)
	require.NoError(t, err)
	require.Zero(t, count, "table %q remains visible after caller rollback", name)
}
