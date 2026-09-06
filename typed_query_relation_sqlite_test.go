package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type countUser struct {
	ID       int64   `rasql:"id"`
	Tenant   int64   `rasql:"tenant"`
	Category *string `rasql:"category"`
}

type countEmail struct {
	Email string `rasql:"email"`
}

type countID struct {
	ID int64 `rasql:"id"`
}

func TestSQLiteTypedReusableCounts(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	database.SetMaxOpenConns(1)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	users, err := rasql.TableOf[countUser](schema.TableDef{Name: "users", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}}, {Name: "category", Type: schema.TextType{}, Nullable: true},
	}})
	require.NoError(t, err)
	require.NoError(t, rasql.CreateTable(t.Context(), db, users))
	_, err = database.ExecContext(t.Context(), `INSERT INTO users VALUES (1, 1, 'a'), (2, 1, NULL), (3, 1, NULL), (4, 2, 'b')`)
	require.NoError(t, err)

	expression := rasql.DecodeFromRef[countEmail](users.Ref()).Project(query.Lower(users.Column("category")).As("email"))
	_, err = expression.Count(t.Context(), db)
	require.ErrorContains(t, err, "result metadata")
	countBase := rasql.SelectFrom(users).WhereEqual(users.Column("tenant"), 1)
	emptyMetadata := rasql.RebindResult[countEmail](countBase, nil, users.Column("category"))
	_, err = emptyMetadata.Count(t.Context(), db)
	require.ErrorContains(t, err, "result columns count")
	mismatchedMetadata := rasql.RebindResult[countEmail](countBase,
		[]query.ResultColumn{{Name: "category", Type: schema.TextType{}}, {Name: "extra", Type: schema.TextType{}}},
		users.Column("category"))
	_, err = mismatchedMetadata.Count(t.Context(), db)
	require.ErrorContains(t, err, "result columns count")

	dtoBase := rasql.SelectFrom(users).WhereEqual(users.Column("tenant"), 2)
	dto := rasql.RebindResult[countEmail](dtoBase,
		[]query.ResultColumn{{Name: "email", Type: schema.TextType{}}}, users.Column("category").As("email"))
	dtoRows, err := dto.All(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, []countEmail{{Email: "b"}}, dtoRows)

	base := rasql.SelectFrom(users).WhereEqual(users.Column("tenant"), 1).OrderAsc(users.Column("id")).Limit(2).Offset(1)
	baseStatement, err := base.Build(dialect.SQLite())
	require.NoError(t, err)
	baseResult, err := base.Result()
	require.NoError(t, err)
	baseColumns := baseResult.Columns()
	baseRows, err := base.All(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, []countUser{{ID: 2, Tenant: 1}, {ID: 3, Tenant: 1}}, baseRows)

	wholeRows, err := base.All(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, baseRows, wholeRows)
	idRows, err := rasql.RebindResult[countID](base, []query.ResultColumn{{Name: "id", Type: schema.IntegerType{}}}, users.Column("id")).All(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, []countID{{ID: 2}, {ID: 3}}, idRows)
	idSelect, err := rasql.RebindResult[countID](base,
		[]query.ResultColumn{{Name: "id", Type: schema.IntegerType{}}}, users.Column("id")).Select()
	require.NoError(t, err)
	outer := rasql.SelectFrom(users).Where(query.InSelect(users.Column("id"), idSelect))
	_, err = outer.Build(dialect.SQLite())
	require.NoError(t, err)
	grouped := rasql.RebindResult[struct{}](base,
		[]query.ResultColumn{{Name: "tenant", Type: schema.IntegerType{}}, {Name: "total", Type: schema.IntegerType{}}},
		users.Column("tenant"), query.CountAll().As("total")).GroupBy(users.Column("tenant"))
	groupCount, err := grouped.Count(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, int64(1), groupCount)
	pageCount, err := base.CountPage(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, int64(2), pageCount)
	afterStatement, err := base.Build(dialect.SQLite())
	require.NoError(t, err)
	afterResult, err := base.Result()
	require.NoError(t, err)
	afterRows, err := base.All(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, baseStatement.SQL(), afterStatement.SQL())
	require.Equal(t, baseStatement.Args(), afterStatement.Args())
	require.Equal(t, baseColumns, afterResult.Columns())
	require.Equal(t, baseRows, afterRows)

	count, err := base.Count(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, int64(3), count)
	pageCount, err = base.CountPage(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, int64(2), pageCount)
	beyond := rasql.SelectFrom(users).WhereEqual(users.Column("tenant"), 1).Limit(2).Offset(20)
	pageCount, err = beyond.CountPage(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, int64(0), pageCount)

	distinct := rasql.DecodeFromRef[struct{}](users.Ref()).Project(users.Column("category")).Distinct()
	distinct = rasql.RebindResult[struct{}](distinct, []query.ResultColumn{{Name: "category", Type: schema.TextType{}, Nullable: true}}, users.Column("category"))
	count, err = distinct.Count(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, int64(3), count)

	groupedWithHaving := rasql.DecodeFromRef[struct{}](users.Ref()).
		Project(users.Column("tenant"), query.CountAll().As("total")).
		GroupBy(users.Column("tenant")).
		Having(query.GreaterThan(query.CountAll(), 1))
	groupedWithHaving = rasql.RebindResult[struct{}](groupedWithHaving, []query.ResultColumn{{Name: "tenant", Type: schema.IntegerType{}}, {Name: "total", Type: schema.IntegerType{}}}, users.Column("tenant"), query.CountAll().As("total"))
	count, err = groupedWithHaving.Count(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
}
