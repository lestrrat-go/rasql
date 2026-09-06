package querydescribe_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/querydescribe"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestSQLiteDescribesAggregateResult(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(context.Background(), `CREATE TABLE users(id INTEGER PRIMARY KEY); CREATE TABLE profiles(user_id INTEGER, nickname TEXT); INSERT INTO users VALUES (1),(2); INSERT INTO profiles VALUES (1,'one')`)
	require.NoError(t, err)
	d := querydescribe.NewSQLite(db)
	got, err := d.Describe(context.Background(), querydescribe.Request{Name: "UserReport", SQL: `SELECT u.id AS user_id, p.nickname AS nickname, count(p.user_id) AS profile_count FROM users AS u LEFT JOIN profiles AS p ON p.user_id = u.id GROUP BY u.id, p.nickname ORDER BY u.id`, Cardinality: querydescribe.Many})
	require.NoError(t, err)
	require.Equal(t, []string{"user_id", "nickname", "profile_count"}, []string{got.Columns[0].Name, got.Columns[1].Name, got.Columns[2].Name})
	require.Equal(t, "int64", got.Columns[0].Binding.Type)
	require.Equal(t, "string", got.Columns[1].Binding.Type)
	require.Equal(t, "int64", got.Columns[2].Binding.Type)
	require.False(t, got.Columns[2].Nullable)
}

func TestSQLiteDescriberRejectsSecondStatement(t *testing.T) {
	d := querydescribe.NewSQLite(nil)
	_, err := d.Describe(context.Background(), querydescribe.Request{Name: "x", SQL: "SELECT 1; SELECT 2"})
	require.ErrorIs(t, err, querydescribe.ErrInvalidRequest)
}
