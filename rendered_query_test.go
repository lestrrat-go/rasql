package rasql_test

import (
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

type renderedRank struct {
	ID    int64
	Email string
	Rank  int64
}

type renderedRankDecoder struct{ schema rasql.ResultSchema }

func (d renderedRankDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (d renderedRankDecoder) Presence() []rasql.Presence       { return nil }
func (d renderedRankDecoder) DecodeRow(source rasql.ScanSource, result *renderedRank) error {
	return source.Scan(&result.ID, &result.Email, &result.Rank)
}

func renderedRankProjection() (rasql.Projection[renderedRank], error) {
	resultSchema, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "email", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "rank", Type: schema.IntegerType{}},
	)
	if err != nil {
		return rasql.Projection[renderedRank]{}, err
	}
	return rasql.NativeProjection(renderedRankDecoder{schema: resultSchema})
}

func renderedRankQuery(s stmt.Statement, engine string, cardinality rasql.Cardinality) (rasql.Query[renderedRank], error) {
	projection, err := renderedRankProjection()
	if err != nil {
		return rasql.Query[renderedRank]{}, err
	}
	args := s.Args()
	nativeArgs := make([]rasql.NativeArgument, len(args))
	for i, arg := range args {
		nativeArgs[i] = rasql.NativeArgument{Value: arg}
	}
	return rasql.Native(rasql.NativeStatement{Engine: engine, SQL: s.SQL(), Args: nativeArgs}, projection, cardinality)
}

func renderedRankExecutor(database rasql.DB, engine string) (rasql.Executor, error) {
	var profile rasql.EngineProfile
	var err error
	switch engine {
	case "postgresql":
		profile, err = rasql.EngineProfileFromVersion("postgresql-17", 17, 0, 0)
	case "sqlite":
		profile, err = rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	default:
		return nil, fmt.Errorf("unsupported test engine %q", engine)
	}
	if err != nil {
		return nil, err
	}
	return rasql.AsExecutor(database, profile)
}

func TestQueryRendered(t *testing.T) {
	t.Run("decodes CTE and window result", testQueryRenderedDecodesCTEAndWindowResult)
	t.Run("All and One use typed decoding", testQueryRenderedAllAndOneUseTypedDecoding)
	t.Run("validates before returning a sequence", testQueryRenderedValidatesBeforeReturningSequence)
	t.Run("rejects whitespace-only SQL", testQueryRenderedRejectsWhitespaceOnlySQL)
}

func testQueryRenderedDecodesCTEAndWindowResult(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	executor, err := renderedRankExecutor(db, "postgresql")
	require.NoError(t, err)
	s := stmt.New(`WITH ranked_users AS (
	SELECT id, email, ROW_NUMBER() OVER (ORDER BY id) AS rank
	FROM users
)
SELECT id, email, rank FROM ranked_users WHERE id >= $1`, 2)
	mock.ExpectQuery(s.SQL()).WithArgs(2).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "rank"}).
			AddRow(int64(2), "bob@example.com", int64(2)))

	query, err := renderedRankQuery(s, "postgresql", rasql.Many)
	require.NoError(t, err)
	found, err := rasql.All(t.Context(), executor, query)
	require.NoError(t, err)
	require.Equal(t, []renderedRank{{ID: 2, Email: "bob@example.com", Rank: 2}}, found)
}

func testQueryRenderedAllAndOneUseTypedDecoding(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	executor, err := renderedRankExecutor(db, "sqlite")
	require.NoError(t, err)
	allStatement := stmt.New("SELECT id, email, rank FROM ranked_users ORDER BY rank")
	mock.ExpectQuery(allStatement.SQL()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "rank"}).
			AddRow(int64(1), "ada@example.com", int64(1)).
			AddRow(int64(2), "bob@example.com", int64(2)))

	allQuery, err := renderedRankQuery(allStatement, "sqlite", rasql.Many)
	require.NoError(t, err)
	all, err := rasql.All(t.Context(), executor, allQuery)
	require.NoError(t, err)
	require.Equal(t, []renderedRank{
		{ID: 1, Email: "ada@example.com", Rank: 1},
		{ID: 2, Email: "bob@example.com", Rank: 2},
	}, all)

	oneStatement := stmt.New("SELECT id, email, rank FROM ranked_users WHERE id = ?", 1)
	mock.ExpectQuery(oneStatement.SQL()).WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "rank"}).
			AddRow(int64(1), "ada@example.com", int64(1)))

	oneQuery, err := renderedRankQuery(oneStatement, "sqlite", rasql.ExactlyOne)
	require.NoError(t, err)
	one, err := rasql.One(t.Context(), executor, oneQuery)
	require.NoError(t, err)
	require.Equal(t, renderedRank{ID: 1, Email: "ada@example.com", Rank: 1}, one)
}

func testQueryRenderedValidatesBeforeReturningSequence(t *testing.T) {
	query, err := renderedRankQuery(stmt.New("SELECT 1"), "sqlite", rasql.Many)
	require.NoError(t, err)
	rows, err := rasql.Rows[renderedRank](t.Context(), nil, query)
	require.Nil(t, rows)
	require.ErrorContains(t, err, "executor")
	_, err = renderedRankQuery(stmt.Statement{}, "sqlite", rasql.Many)
	require.ErrorContains(t, err, "must not be empty")
}

func testQueryRenderedRejectsWhitespaceOnlySQL(t *testing.T) {
	_, err := renderedRankQuery(stmt.New("   "), "sqlite", rasql.Many)
	require.ErrorContains(t, err, "must not be empty")
}
