package rasql_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/statement"
	"github.com/stretchr/testify/require"
)

type renderedRank struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
	Rank  int64  `rasql:"rank"`
}

func TestQueryRenderedDecodesCTEAndWindowResult(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	stmt := statement.New(`WITH ranked_users AS (
	SELECT id, email, ROW_NUMBER() OVER (ORDER BY id) AS rank
	FROM users
)
SELECT id, email, rank FROM ranked_users WHERE id >= $1`, 2)
	mock.ExpectQuery(stmt.SQL()).WithArgs(2).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "rank"}).
			AddRow(int64(2), "bob@example.com", int64(2)))

	rows, err := rasql.QueryRendered[renderedRank](t.Context(), db, stmt)
	require.NoError(t, err)
	var found []renderedRank
	for value, err := range rows {
		require.NoError(t, err)
		found = append(found, value)
	}
	require.Equal(t, []renderedRank{{ID: 2, Email: "bob@example.com", Rank: 2}}, found)
}

func TestQueryRenderedAllAndOneUseTypedDecoding(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	allStatement := statement.New("SELECT id, email, rank FROM ranked_users ORDER BY rank")
	mock.ExpectQuery(allStatement.SQL()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "rank"}).
			AddRow(int64(1), "ada@example.com", int64(1)).
			AddRow(int64(2), "bob@example.com", int64(2)))

	all, err := rasql.QueryRenderedAll[renderedRank](t.Context(), db, allStatement)
	require.NoError(t, err)
	require.Equal(t, []renderedRank{
		{ID: 1, Email: "ada@example.com", Rank: 1},
		{ID: 2, Email: "bob@example.com", Rank: 2},
	}, all)

	oneStatement := statement.New("SELECT id, email, rank FROM ranked_users WHERE id = ?", 1)
	mock.ExpectQuery(oneStatement.SQL()).WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "rank"}).
			AddRow(int64(1), "ada@example.com", int64(1)))

	one, err := rasql.QueryRenderedOne[renderedRank](t.Context(), db, oneStatement)
	require.NoError(t, err)
	require.Equal(t, renderedRank{ID: 1, Email: "ada@example.com", Rank: 1}, one)
}

func TestQueryRenderedValidatesBeforeReturningSequence(t *testing.T) {
	var db rasql.DB
	stmt := statement.New("SELECT 1")

	rows, err := rasql.QueryRendered[renderedRank](t.Context(), db, stmt)
	require.Nil(t, rows)
	require.ErrorContains(t, err, "rasql: invalid DB")

	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err = rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	rows, err = rasql.QueryRendered[renderedRank](t.Context(), db, statement.Statement{})
	require.Nil(t, rows)
	require.ErrorContains(t, err, "rasql: statement SQL must not be empty")
}

// TestQueryRenderedRejectsWhitespaceOnlySQL pins that a statement whose SQL is
// only whitespace is rejected the same way an empty one is, now that the
// blank check lives in DB.validStatement rather than in a constructor that
// used to trim before storing. This is where render.Precompiled's own
// blank-SQL subtests landed after Precompiled was deleted.
func TestQueryRenderedRejectsWhitespaceOnlySQL(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)

	rows, err := rasql.QueryRendered[renderedRank](t.Context(), db, statement.New("   "))
	require.Nil(t, rows)
	require.ErrorContains(t, err, "rasql: statement SQL must not be empty")
}
