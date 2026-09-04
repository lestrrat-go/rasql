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
)

// TestSQLiteOrdersByOrderResultAlias proves query.AscResult/DescResult
// executes against a real database, rather than only validating and
// rendering: the row order it returns is the order the aliased projection
// sorts by, not the order the underlying table happens to store rows in.
func TestSQLiteOrdersByOrderResultAlias(t *testing.T) {
	database, definition := orderResultAliasFixture(t)
	table, err := query.NewTableRef(definition)
	require.NoError(t, err)
	location := table.Column("city").As("location")

	statement, err := query.NewSelect(table, location)
	require.NoError(t, err)
	statement, err = statement.WithOrder(query.AscResult(location))
	require.NoError(t, err)
	rendered, err := render.Select(dialect.SQLite(), statement)
	require.NoError(t, err)
	require.Equal(t,
		`SELECT "`+definition.Name+`"."city" AS "location" FROM "`+definition.Name+`" ORDER BY "location"`,
		rendered.SQL())

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
	// The fixture inserts tokyo, osaka, tokyo (see orderResultAliasFixture),
	// so ordering by the "location" alias ascending sorts osaka before tokyo.
	require.Equal(t, []string{"osaka", "tokyo", "tokyo"}, cities)

	descending, err := query.NewSelect(table, location)
	require.NoError(t, err)
	descending, err = descending.WithOrder(query.DescResult(location))
	require.NoError(t, err)
	renderedDescending, err := render.Select(dialect.SQLite(), descending)
	require.NoError(t, err)

	var descendingCities []string
	rows, err = database.QueryContext(t.Context(), renderedDescending.SQL(), renderedDescending.Args()...)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var c string
		require.NoError(t, rows.Scan(&c))
		descendingCities = append(descendingCities, c)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{"tokyo", "tokyo", "osaka"}, descendingCities)
}

// TestSQLiteRefusesAmbiguousOrderResultAlias proves decision 2's premise on
// the one dialect that would otherwise hide the problem: SQLite silently
// resolves "SELECT id, name AS id FROM u ORDER BY id" to name, rather than
// erroring the way PostgreSQL and MySQL do (see
// order_result_alias_integration_test.go for both of those). rasql refuses
// to build the statement at all, in Go, before it ever reaches a database
// that would answer a question the caller did not ask.
func TestSQLiteRefusesAmbiguousOrderResultAlias(t *testing.T) {
	_, definition := orderResultAliasFixture(t)
	table, err := query.NewTableRef(definition)
	require.NoError(t, err)
	id := table.Column("id")
	aliasedCity := table.Column("city").As("id")

	statement, err := query.NewSelect(table, id, aliasedCity)
	require.NoError(t, err)
	_, err = statement.WithOrder(query.AscResult(aliasedCity))
	require.Error(t, err)
	require.ErrorContains(t, err, "ambiguous")
}

// orderResultAliasFixture opens an in-memory SQLite database holding three
// rows across two cities, so an alias ordering has a real duplicate value to
// sort and a real per-row difference to observe.
func orderResultAliasFixture(t *testing.T) (*sql.DB, schema.TableDef) {
	t.Helper()

	definition := schema.TableDef{
		Name: "order_result_alias_people",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "city", Type: schema.TextType{}},
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

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	type person struct {
		ID   int64  `rasql:"id"`
		City string `rasql:"city"`
	}
	people, err := rasql.TableOf[person](definition)
	require.NoError(t, err)
	require.NoError(t, rasql.CreateTable(t.Context(), db, people))
	for _, fixture := range []person{
		{ID: 1, City: "tokyo"},
		{ID: 2, City: "osaka"},
		{ID: 3, City: "tokyo"},
	} {
		_, err = rasql.Insert(t.Context(), db, people, fixture)
		require.NoError(t, err)
	}
	return database, definition
}
