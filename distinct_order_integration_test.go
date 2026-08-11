//go:build unix

package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// distinctOrderCase describes one live server the ORDER BY-vs-DISTINCT
// decision at query/select.go's WithDistinct is proved against.
type distinctOrderCase struct {
	name    string
	open    func(*testing.T) *sql.DB
	dialect dialect.Dialect
}

// TestDistinctOrderAgainstLiveDatabases proves the reason rasql leaves an
// unprojected ORDER BY on a distinct statement to the database rather than
// refusing it in Go: PostgreSQL and MySQL both refuse the shape at the
// server, with SQLSTATE 42P10 and error 3065 ER_FIELD_IN_ORDER_NOT_SELECT,
// so a rasql-rendered statement never reaches a silently wrong answer on
// either of them. TestSQLiteAnswersUnprojectedDistinctOrderArbitrarily covers
// the third dialect, which runs the same shape instead of refusing it. Each
// case skips when its server is unavailable; CI's integration job runs both.
func TestDistinctOrderAgainstLiveDatabases(t *testing.T) {
	for _, test := range []distinctOrderCase{
		{name: "postgresql", open: dbtest.PostgreSQLDB, dialect: dialect.PostgreSQL()},
		{name: "mysql", open: dbtest.MySQLDB, dialect: dialect.MySQL()},
	} {
		t.Run(test.name, func(t *testing.T) {
			testDistinctOrder(t, test.open(t), test)
		})
	}
}

func testDistinctOrder(t *testing.T, database *sql.DB, test distinctOrderCase) {
	client, err := rasql.New(database, test.dialect)
	require.NoError(t, err)
	type record struct {
		ID   int64  `rasql:"id"`
		City string `rasql:"city"`
		Age  int64  `rasql:"age"`
	}
	// A per-run unique name keeps this test from ever dropping a table it did
	// not create, for the reason testDatabaseIntegration records.
	tableName := dbtest.UniqueName(t, "rasql_distinct_order_records")
	definition := schema.TableDef{
		Name: tableName,
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "city", Type: schema.TextType{}},
			{Name: "age", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	}
	records, err := rasql.TableOf[record](definition)
	require.NoError(t, err)

	_, err = database.ExecContext(t.Context(), "DROP TABLE IF EXISTS "+tableName)
	require.NoError(t, err)
	defer func() {
		_, err := database.ExecContext(t.Context(), "DROP TABLE IF EXISTS "+tableName)
		require.NoError(t, err)
	}()
	require.NoError(t, rasql.Create(t.Context(), client, records))
	for _, fixture := range []record{
		{ID: 1, City: "tokyo", Age: 30},
		{ID: 2, City: "osaka", Age: 20},
		{ID: 3, City: "tokyo", Age: 10},
	} {
		_, err = rasql.Insert(t.Context(), client, records, fixture)
		require.NoError(t, err)
	}

	table, err := query.NewTable(definition)
	require.NoError(t, err)
	city, err := table.Column("city")
	require.NoError(t, err)
	age, err := table.Column("age")
	require.NoError(t, err)

	t.Run("the server refuses ORDER BY on a column the distinct projections do not select", func(t *testing.T) {
		// query.Select places no Go-side rule here (see the WithDistinct doc
		// comment at query/select.go), so the builder renders this statement
		// and lets the server report its own error rather than refusing it
		// before rendering, the way it does for a misplaced aggregate.
		statement, err := query.NewSelect(table, query.Project(city))
		require.NoError(t, err)
		statement, err = statement.WithDistinct()
		require.NoError(t, err)
		statement, err = statement.WithOrder(query.Asc(age))
		require.NoError(t, err)
		rendered, err := render.Select(test.dialect, statement)
		require.NoError(t, err, "rendering succeeds; the database is what refuses the statement")

		rows, err := database.QueryContext(t.Context(), rendered.SQL(), rendered.Args()...)
		if err == nil {
			_ = rows.Close()
		}
		require.Error(t, err, "%s must refuse ORDER BY on a column outside the distinct projections", test.name)
	})

	t.Run("the database runs a distinct statement ordered by a projected column", func(t *testing.T) {
		statement, err := query.NewSelect(table, query.Project(city))
		require.NoError(t, err)
		statement, err = statement.WithDistinct()
		require.NoError(t, err)
		statement, err = statement.WithOrder(query.Asc(city))
		require.NoError(t, err)
		rendered, err := render.Select(test.dialect, statement)
		require.NoError(t, err)

		var cities []string
		rows, err := database.QueryContext(t.Context(), rendered.SQL(), rendered.Args()...)
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var c string
			require.NoError(t, rows.Scan(&c))
			cities = append(cities, c)
		}
		require.NoError(t, rows.Err())
		require.Equal(t, []string{"osaka", "tokyo"}, cities)
	})
}
