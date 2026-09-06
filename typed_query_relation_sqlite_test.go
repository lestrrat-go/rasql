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
	ID       int64  `rasql:"id"`
	Tenant   int64  `rasql:"tenant"`
	Category string `rasql:"category"`
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

	base := rasql.SelectFrom(users).WhereEqual(users.Column("tenant"), 1).OrderAsc(users.Column("id")).Limit(2).Offset(1)
	count, err := base.Count(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, int64(3), count)
	pageCount, err := base.CountPage(t.Context(), db)
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

	grouped := rasql.DecodeFromRef[struct{}](users.Ref()).
		Project(users.Column("tenant"), query.CountAll().As("total")).
		GroupBy(users.Column("tenant")).
		Having(query.GreaterThan(query.CountAll(), 1))
	grouped = rasql.RebindResult[struct{}](grouped, []query.ResultColumn{{Name: "tenant", Type: schema.IntegerType{}}, {Name: "total", Type: schema.IntegerType{}}}, users.Column("tenant"), query.CountAll().As("total"))
	count, err = grouped.Count(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
}
