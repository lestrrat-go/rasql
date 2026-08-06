package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// TestSQLiteRunsDistinctStatements proves SELECT DISTINCT executes against a
// real database and de-duplicates result rows, including the distinct-then-
// page shape a caller uses for a value list.
func TestSQLiteRunsDistinctStatements(t *testing.T) {
	database, definition := distinctFixture(t)
	table, err := query.NewTable(definition)
	require.NoError(t, err)
	city, err := table.Column("city")
	require.NoError(t, err)

	t.Run("distinct removes duplicate rows", func(t *testing.T) {
		statement, err := query.NewSelect(table, query.Project(city))
		require.NoError(t, err)
		statement, err = statement.WithDistinct()
		require.NoError(t, err)
		statement, err = statement.WithOrder(query.Asc(city))
		require.NoError(t, err)
		rendered, err := render.Select(dialect.SQLite(), statement)
		require.NoError(t, err)

		var cities []string
		rows, err := database.QueryContext(t.Context(), rendered.SQL(), rendered.Args()...)
		require.NoError(t, err)
		defer rows.Close()
		for rows.Next() {
			var c string
			require.NoError(t, rows.Scan(&c))
			cities = append(cities, c)
		}
		require.NoError(t, rows.Err())
		require.Equal(t, []string{"osaka", "tokyo"}, cities, "three fixture rows share two distinct cities")
	})

	t.Run("distinct then page keeps the deduplicated order", func(t *testing.T) {
		statement, err := query.NewSelect(table, query.Project(city))
		require.NoError(t, err)
		statement, err = statement.WithDistinct()
		require.NoError(t, err)
		statement, err = statement.WithOrder(query.Asc(city))
		require.NoError(t, err)
		statement, err = statement.WithLimit(1)
		require.NoError(t, err)
		statement, err = statement.WithOffset(1)
		require.NoError(t, err)
		rendered, err := render.Select(dialect.SQLite(), statement)
		require.NoError(t, err)

		var c string
		result := database.QueryRowContext(t.Context(), rendered.SQL(), rendered.Args()...)
		require.NoError(t, result.Scan(&c))
		require.Equal(t, "tokyo", c)
	})

	t.Run("COUNT(DISTINCT column) counts distinct values", func(t *testing.T) {
		statement, err := query.NewSelect(table, query.Project(query.Count(city).WithDistinct()).As("distinct_cities"))
		require.NoError(t, err)
		rendered, err := render.Select(dialect.SQLite(), statement)
		require.NoError(t, err)

		var count int64
		result := database.QueryRowContext(t.Context(), rendered.SQL(), rendered.Args()...)
		require.NoError(t, result.Scan(&count))
		require.Equal(t, int64(2), count)
	})
}

// TestSQLiteAnswersUnprojectedDistinctOrderArbitrarily proves the rationale
// behind the maintainer decision documented at query/select.go's WithDistinct:
// rasql renders a DISTINCT statement ordered by a column outside its
// projections rather than refusing it in Go, because SQLite runs it and
// answers from whichever row survived de-duplication -- no question the
// caller asked. distinct_order_integration_test.go proves that PostgreSQL and
// MySQL instead refuse the same shape at the server, with SQLSTATE 42P10 and
// error 3065.
func TestSQLiteAnswersUnprojectedDistinctOrderArbitrarily(t *testing.T) {
	database, definition := distinctFixture(t)
	table, err := query.NewTable(definition)
	require.NoError(t, err)
	city, err := table.Column("city")
	require.NoError(t, err)
	age, err := table.Column("age")
	require.NoError(t, err)

	// The builder renders this statement rather than refusing it: rasql places
	// no Go-side rule on which ORDER BY expressions a distinct statement may
	// use.
	statement, err := query.NewSelect(table, query.Project(city))
	require.NoError(t, err)
	statement, err = statement.WithDistinct()
	require.NoError(t, err)
	statement, err = statement.WithOrder(query.Asc(age))
	require.NoError(t, err)
	rendered, err := render.Select(dialect.SQLite(), statement)
	require.NoError(t, err)
	require.Equal(t, `SELECT DISTINCT "`+definition.Name+`"."city" FROM "`+definition.Name+`" ORDER BY "`+definition.Name+`"."age"`, rendered.SQL())

	var cities []string
	rows, err := database.QueryContext(t.Context(), rendered.SQL(), rendered.Args()...)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var c string
		require.NoError(t, rows.Scan(&c))
		cities = append(cities, c)
	}
	require.NoError(t, rows.Err())
	// SQLite answers the statement rather than refusing it, in whichever order
	// its query planner happens to keep the surviving rows -- the point being
	// proven is that it runs at all, not which order it picks.
	require.ElementsMatch(t, []string{"osaka", "tokyo"}, cities)
}

// distinctFixture opens an in-memory SQLite database holding three users
// across two cities and distinct ages, so DISTINCT city has a real duplicate
// to remove and ORDER BY age has a real per-row difference from ORDER BY city.
func distinctFixture(t *testing.T) (*sql.DB, schema.Table) {
	t.Helper()

	definition := schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "city", Type: schema.TypeText},
			{Name: "age", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	}

	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	// An in-memory SQLite database is per connection, so keep the test on one.
	database.SetMaxOpenConns(1)

	client, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	type user struct {
		ID   int64  `rasql:"id"`
		City string `rasql:"city"`
		Age  int64  `rasql:"age"`
	}
	users, err := rasql.NewTable[user](definition)
	require.NoError(t, err)
	require.NoError(t, rasql.Create(t.Context(), client, users))
	for _, fixture := range []user{
		{ID: 1, City: "tokyo", Age: 30},
		{ID: 2, City: "osaka", Age: 20},
		{ID: 3, City: "tokyo", Age: 10},
	} {
		_, err = rasql.Insert(t.Context(), client, users, fixture)
		require.NoError(t, err)
	}
	return database, definition
}
