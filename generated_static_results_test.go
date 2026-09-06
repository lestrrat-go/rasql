package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type userReportRow struct {
	UserID       int64
	Nickname     *string
	ProfileCount int64
}

func statement(text string) stmt.Statement { return stmt.New(sqltext.Text(text)) }

func (r *userReportRow) ScanDestinations(columns []string) ([]any, error) {
	dest := make([]any, len(columns))
	for i, column := range columns {
		switch column {
		case "user_id":
			dest[i] = &r.UserID
		case "nickname":
			dest[i] = &r.Nickname
		case "profile_count":
			dest[i] = &r.ProfileCount
		default:
			return nil, sql.ErrNoRows
		}
	}
	return dest, nil
}

func TestGeneratedStaticLeftJoinAndCardinality(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	database.SetMaxOpenConns(1)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE users(id INTEGER PRIMARY KEY); CREATE TABLE profiles(user_id INTEGER, nickname TEXT); INSERT INTO users VALUES (1),(2); INSERT INTO profiles VALUES (1,'one')`)
	require.NoError(t, err)
	sqlText := `SELECT u.id AS user_id, p.nickname AS nickname, count(p.user_id) AS profile_count FROM users AS u LEFT JOIN profiles AS p ON p.user_id = u.id GROUP BY u.id, p.nickname ORDER BY u.id`
	rows, err := rasql.QueryRendered[userReportRow](t.Context(), db, statement(sqlText))
	require.NoError(t, err)
	var got []userReportRow
	for row, rowErr := range rows {
		require.NoError(t, rowErr)
		got = append(got, row)
	}
	require.Len(t, got, 2)
	require.Equal(t, int64(1), got[0].UserID)
	require.Equal(t, "one", *got[0].Nickname)
	require.Equal(t, int64(1), got[0].ProfileCount)
	require.Equal(t, int64(2), got[1].UserID)
	require.Nil(t, got[1].Nickname)
	require.Equal(t, int64(0), got[1].ProfileCount)

	one, found, err := rasql.QueryRenderedOptional[userReportRow](t.Context(), db, statement("SELECT id AS user_id, NULL AS nickname, 0 AS profile_count FROM users WHERE id = 99"))
	require.NoError(t, err)
	require.False(t, found)
	require.Zero(t, one)
	one, found, err = rasql.QueryRenderedOptional[userReportRow](t.Context(), db, statement("SELECT id AS user_id, NULL AS nickname, 0 AS profile_count FROM users WHERE id = 1"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(1), one.UserID)
	_, _, err = rasql.QueryRenderedOptional[userReportRow](t.Context(), db, statement("SELECT id AS user_id, NULL AS nickname, 0 AS profile_count FROM users"))
	require.ErrorIs(t, err, rasql.ErrMultipleRows)
	_, err = rasql.QueryRenderedOne[userReportRow](t.Context(), db, statement("SELECT id AS user_id, NULL AS nickname, 0 AS profile_count FROM users WHERE id = 99"))
	require.ErrorIs(t, err, rasql.ErrNoRows)
	_, err = rasql.QueryRenderedOne[userReportRow](t.Context(), db, statement("SELECT id AS user_id, NULL AS nickname, 0 AS profile_count FROM users"))
	require.ErrorIs(t, err, rasql.ErrMultipleRows)
}
