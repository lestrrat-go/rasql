package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type userReportRow struct {
	UserID       int64
	Nickname     *string
	ProfileCount int64
}

type userReportDecoder struct{ schema rasql.ResultSchema }

func (d userReportDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (userReportDecoder) Presence() []rasql.Presence         { return nil }
func (d userReportDecoder) DecodeRow(source rasql.ScanSource, row *userReportRow) error {
	return source.Scan(&row.UserID, &row.Nickname, &row.ProfileCount)
}

func userReportProjection() (rasql.Projection[userReportRow], error) {
	resultSchema, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "user_id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "nickname", Type: schema.TextType{}, Nullable: true},
		rasql.ResultColumn{Name: "profile_count", Type: schema.IntegerType{}},
	)
	if err != nil {
		return rasql.Projection[userReportRow]{}, err
	}
	return rasql.NativeProjection(userReportDecoder{schema: resultSchema})
}

// nativeUserReportQuery adapts a static SQL template to the canonical typed
// API, the way generated code that reads a hand-written statement does. The
// cardinality here is always Many: Rows, One and Maybe each install their own
// stricter requirement over it, and a Native query with no rows already
// defaults an ExactlyOne caller to ErrNoRows regardless of what cardinality
// the query itself declared.
func nativeUserReportQuery(sqlText string) (rasql.Query[userReportRow], error) {
	projection, err := userReportProjection()
	if err != nil {
		return rasql.Query[userReportRow]{}, err
	}
	return rasql.Native(rasql.NativeStatement{Engine: "sqlite", SQL: sqlText}, projection, rasql.Many)
}

func TestGeneratedStaticLeftJoinAndCardinality(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	database.SetMaxOpenConns(1)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE users(id INTEGER PRIMARY KEY); CREATE TABLE profiles(user_id INTEGER, nickname TEXT); INSERT INTO users VALUES (1),(2); INSERT INTO profiles VALUES (1,'one')`)
	require.NoError(t, err)
	sqlText := `SELECT u.id AS user_id, p.nickname AS nickname, count(p.user_id) AS profile_count FROM users AS u LEFT JOIN profiles AS p ON p.user_id = u.id GROUP BY u.id, p.nickname ORDER BY u.id`
	joinQuery, err := nativeUserReportQuery(sqlText)
	require.NoError(t, err)
	rows, err := rasql.Rows(t.Context(), executor, joinQuery)
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

	noneQuery, err := nativeUserReportQuery("SELECT id AS user_id, NULL AS nickname, 0 AS profile_count FROM users WHERE id = 99")
	require.NoError(t, err)
	one, found, err := rasql.Maybe(t.Context(), executor, noneQuery)
	require.NoError(t, err)
	require.False(t, found)
	require.Zero(t, one)
	oneQuery, err := nativeUserReportQuery("SELECT id AS user_id, NULL AS nickname, 0 AS profile_count FROM users WHERE id = 1")
	require.NoError(t, err)
	one, found, err = rasql.Maybe(t.Context(), executor, oneQuery)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(1), one.UserID)
	manyQuery, err := nativeUserReportQuery("SELECT id AS user_id, NULL AS nickname, 0 AS profile_count FROM users")
	require.NoError(t, err)
	_, _, err = rasql.Maybe(t.Context(), executor, manyQuery)
	require.ErrorIs(t, err, rasql.ErrMultipleRows)
	_, err = rasql.One(t.Context(), executor, noneQuery)
	require.ErrorIs(t, err, rasql.ErrNoRows)
	oneExact, err := rasql.One(t.Context(), executor, oneQuery)
	require.NoError(t, err)
	require.Equal(t, int64(1), oneExact.UserID)
	_, err = rasql.One(t.Context(), executor, manyQuery)
	require.ErrorIs(t, err, rasql.ErrMultipleRows)
}
