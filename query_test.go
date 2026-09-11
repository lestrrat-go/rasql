package rasql_test

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

type queryAPIDecoder struct {
	resultSchema rasql.ResultSchema
	presence     []rasql.Presence
}

func (d queryAPIDecoder) ResultSchema() rasql.ResultSchema { return d.resultSchema }
func (d queryAPIDecoder) Presence() []rasql.Presence {
	return append([]rasql.Presence(nil), d.presence...)
}
func (d queryAPIDecoder) DecodeRow(source rasql.ScanSource, result *int64) error {
	return source.Scan(result)
}

type queryAPISource struct{ value any }

func (s *queryAPISource) Scan(destinations ...any) error {
	if len(destinations) != 1 {
		return sql.ErrNoRows
	}
	return rasql.ScanValue(destinations[0].(*int64), s.value)
}

func TestQueryAPI(t *testing.T) {
	t.Run("a result schema copies defensively and validates", func(t *testing.T) {
		columns := []rasql.ResultColumn{{Name: "id", Type: schema.IntegerType{}}}
		result, err := rasql.NewResultSchema(columns...)
		require.NoError(t, err)
		columns[0].Name = "changed"
		require.Equal(t, "id", result.Columns()[0].Name)
		copy := result.Columns()
		copy[0].Name = "changed"
		require.Equal(t, "id", result.Columns()[0].Name)
		for _, column := range []rasql.ResultColumn{{}, {Name: "id"}, {Name: "id", Type: schema.IntegerType{}, Codec: "bad codec"}} {
			_, err := rasql.NewResultSchema(column)
			require.Error(t, err, "accepted invalid column %#v", column)
		}
		_, err = rasql.NewResultSchema(
			rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
			rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		)
		require.Error(t, err)
	})

	t.Run("a result schema rejects a pointer column type", func(t *testing.T) {
		_, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: &schema.IntegerType{}})
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "invalid_schema", planErr.Code)
		require.Equal(t, "columns[0].type", planErr.Path)
		require.Equal(t, "unsupported column type *schema.IntegerType", planErr.Detail)
	})

	t.Run("projection validation and presence", func(t *testing.T) {
		resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
		require.NoError(t, err)
		decoder := queryAPIDecoder{resultSchema: resultSchema}
		item := rasql.Item("id", rasql.Value(int64(1)), schema.IntegerType{}, "")
		projection, err := rasql.NewProjection([]rasql.ProjectionItem{item}, decoder)
		require.NoError(t, err)
		require.Len(t, projection.Schema().Columns(), 1)
		wrongSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "other", Type: schema.IntegerType{}})
		require.NoError(t, err)
		_, err = rasql.NewProjection([]rasql.ProjectionItem{item}, queryAPIDecoder{resultSchema: wrongSchema})
		require.Error(t, err)
		presence, err := rasql.NewPresence("profile", "id")
		require.NoError(t, err)
		nullSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}, Nullable: true})
		require.NoError(t, err)
		nullItem := rasql.NullItem("id", rasql.MinExpr(rasql.Value(int64(1))), schema.IntegerType{}, "")
		_, err = rasql.NewProjection([]rasql.ProjectionItem{nullItem}, queryAPIDecoder{resultSchema: nullSchema, presence: []rasql.Presence{presence}})
		require.NoError(t, err)
		unknown, err := rasql.NewPresence("profile", "missing")
		require.NoError(t, err)
		_, err = rasql.NewProjection([]rasql.ProjectionItem{item}, queryAPIDecoder{resultSchema: resultSchema, presence: []rasql.Presence{unknown}})
		require.Error(t, err)
	})

	t.Run("a scalar decoder uses a fresh destination", func(t *testing.T) {
		projection, err := rasql.Scalar("value", rasql.Value(int64(1)), schema.IntegerType{}, "")
		require.NoError(t, err)
		first, second := int64(0), int64(0)
		source := &queryAPISource{value: int64(42)}
		require.NoError(t, projection.Decoder().DecodeRow(source, &first))
		source.value = int64(7)
		require.NoError(t, projection.Decoder().DecodeRow(source, &second))
		require.Equal(t, int64(42), first)
		require.Equal(t, int64(7), second)
	})

	t.Run("query operations are immutable", func(t *testing.T) {
		table, err := rasql.ReadTableOf[int64](schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
		require.NoError(t, err)
		relation, err := rasql.SourceOf(table, "u")
		require.NoError(t, err)
		projection, err := rasql.Scalar("value", rasql.Value(int64(1)), schema.IntegerType{}, "")
		require.NoError(t, err)
		base := rasql.Select(relation.Source(), projection)
		filtered := base.Where(rasql.EqualValue(rasql.Value(int64(1)), int64(1)))
		require.NoError(t, base.Validate())
		require.NoError(t, filtered.Validate())
		_, err = base.Limit(-1)
		require.Error(t, err)
		_, err = base.Offset(-1)
		require.Error(t, err)
	})

	t.Run("source-bound columns validate nullability and membership", func(t *testing.T) {
		table, err := rasql.ReadTableOf[int64](schema.TableDef{Name: "users", Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "nickname", Type: schema.TextType{}, Nullable: true},
		}})
		require.NoError(t, err)
		relation, err := rasql.SourceOf(table, "u")
		require.NoError(t, err)
		column, err := rasql.BindColumn[int64, int64](relation, "id", "")
		require.NoError(t, err)
		require.NotNil(t, column.Expr())
		nullable, err := rasql.BindNullColumn[int64, string](relation, "nickname", "custom.codec")
		require.NoError(t, err)
		require.NotNil(t, nullable.NullExpr())
		_, err = rasql.BindOptionalColumn[int64, int64](rasql.Optional(relation), "id", "")
		require.NoError(t, err)
		_, err = rasql.BindColumn[int64, int64](relation, "nickname", "")
		require.Error(t, err)
		_, err = rasql.BindColumn[int64, int64](relation, "missing", "")
		require.Error(t, err)
	})
}

type compositionRow struct{ ID int64 }
type compositionDecoder struct{ schema rasql.ResultSchema }

func (d compositionDecoder) ResultSchema() rasql.ResultSchema                { return d.schema }
func (compositionDecoder) Presence() []rasql.Presence                        { return nil }
func (compositionDecoder) DecodeRow(rasql.ScanSource, *compositionRow) error { return nil }

func compositionQuery(t *testing.T) rasql.Query[compositionRow] {
	t.Helper()
	s, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	p, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", rasql.Value(int64(1)), schema.IntegerType{}, ""),
	}, compositionDecoder{schema: s})
	require.NoError(t, err)
	table, err := rasql.ReadTableOf[compositionRow](schema.TableDef{
		Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "u")
	require.NoError(t, err)
	return rasql.Select(relation.Source(), p)
}

func TestComposition(t *testing.T) {
	t.Run("preserves typed results", func(t *testing.T) {
		base := compositionQuery(t)
		derived, err := rasql.Derive(base, "d")
		require.NoError(t, err)
		require.NotEqual(t, rasql.Source{}, derived.Source())
		bound, err := rasql.BindResultColumn[compositionRow, int64](derived, "id")
		require.NoError(t, err)
		_ = bound
		cte, err := rasql.CTEOf("items", base)
		require.NoError(t, err)
		cteSource, err := cte.Source("i")
		require.NoError(t, err)
		require.NotEqual(t, rasql.Source{}, cteSource.Source())
		combined, err := rasql.Combine(base, rasql.UnionAll, base)
		require.NoError(t, err)
		require.NoError(t, combined.Validate())
		paged, err := combined.Limit(1)
		require.NoError(t, err)
		require.NoError(t, paged.Validate())
		count := rasql.CountQuery(base, false)
		require.NoError(t, count.Validate())
		with, err := rasql.With(base, cte)
		require.NoError(t, err)
		require.NoError(t, with.Validate())
	})

	t.Run("rejects invalid aliases and schemas", func(t *testing.T) {
		base := compositionQuery(t)
		_, err := rasql.Derive(base, "bad alias")
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "invalid_source", planErr.Code)
		_, err = rasql.CTEOf("bad alias", base)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "invalid_cte", planErr.Code)
	})
}

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
